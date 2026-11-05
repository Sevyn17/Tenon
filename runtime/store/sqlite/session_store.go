package sqlite

// SessionStore 实现（data-contract §4.2–§4.4，review-1 R1/R4/R5）。
//
// 头部 JSON 不含 Config：Config 按内容寻址存 session_shells，sessions.shell_hash 引用它（R5）。
// 条目按完整日志下标 idx 存；每条分配会话级单调 sync_seq（R4），撤销后不复用。
// checkpoint 是一个事务：截断入归档 → 追加条目 → 外壳 → 请求记录 → inbox 删除 → span → 头部条件写。

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"ybase/runtime/bus"
	"ybase/runtime/session"
	"ybase/runtime/store"
)

func jsonObj(b json.RawMessage) string {
	if len(b) == 0 {
		return "{}"
	}
	return string(b)
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// CreateSession 新建会话（头部 + 外壳 + 可选初始条目，一个事务）。
func (db *DB) CreateSession(ctx context.Context, m store.SessionMeta, hdr session.Header, entries []*session.Entry) error {
	if m.Origin == "" {
		m.Origin = "new"
	}
	now := time.Now().UnixMilli()
	return db.inTx(ctx, func(tx *sql.Tx) error {
		shell, hb, err := splitHeader(hdr)
		if err != nil {
			return err
		}
		if err := putShell(ctx, tx, shell); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO sessions(tenant, id, agent_id, agent_hash, creator, parent_id, origin, fork_upto,
			title, header, shell_hash, session_key, meta, use_config, log_len, next_sync_seq, updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,0,1,?)`,
			store.Tenant, m.ID, m.AgentID, m.AgentHash, m.Creator, m.ParentID, m.Origin, m.ForkUpto, m.Title, hb, shell.hash,
			nullStr(m.SessionKey), jsonObj(m.Meta), jsonObj(m.UseConfig), now); err != nil {
			if isUniqueViolation(err) {
				return fmt.Errorf("%w: 会话 %s 或会话键已存在", store.ErrConflict, m.ID)
			}
			return err
		}
		if len(entries) == 0 {
			return nil
		}
		seq, err := appendEntries(ctx, tx, m.ID, 0, entries, 1)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE sessions SET log_len = ?, next_sync_seq = ? WHERE tenant = ? AND id = ?`, len(entries), seq, store.Tenant, m.ID)
		return err
	})
}

// Commit 执行一次 checkpoint。条件写失败返回 ErrConflict，事务整体回滚。
func (db *DB) Commit(ctx context.Context, cp store.Checkpoint) error {
	now := time.Now().UnixMilli()
	var evs []bus.Envelope
	err := db.inTx(ctx, func(tx *sql.Tx) error {
		var logLen, seq int
		var leaseEpoch int64
		err := tx.QueryRowContext(ctx, `SELECT log_len, next_sync_seq, lease_epoch FROM sessions WHERE id = ? AND tenant = ?`, cp.SessionID, store.Tenant).Scan(&logLen, &seq, &leaseEpoch)
		if errors.Is(err, sql.ErrNoRows) {
			return store.ErrNotFound
		}
		if err != nil {
			return err
		}
		// 先校验纪元再校验 log_len：接管者推进过日志后，旧持有者的迟到写入两条都不符，
		// 应报租约失效而不是普通冲突（02 2.2 fencing）。
		if cp.Epoch != 0 && leaseEpoch != cp.Epoch {
			return fmt.Errorf("%w: 期望纪元 %d，实际 %d", store.ErrLeaseLost, cp.Epoch, leaseEpoch)
		}
		if logLen != cp.ExpectLogLen {
			return fmt.Errorf("%w: 期望 log_len=%d，实际 %d", store.ErrConflict, cp.ExpectLogLen, logLen)
		}
		if cp.TruncateFrom != nil {
			if _, err := tx.ExecContext(ctx, `INSERT INTO session_entries_archive(tenant, session_id, idx, id, body, truncated_at, rewind_seq)
				SELECT tenant, session_id, idx, id, body, ?, ? FROM session_entries WHERE tenant = ? AND session_id = ? AND idx >= ?`,
				now, seq, store.Tenant, cp.SessionID, *cp.TruncateFrom); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM session_entries WHERE tenant = ? AND session_id = ? AND idx >= ?`, store.Tenant, cp.SessionID, *cp.TruncateFrom); err != nil {
				return err
			}
			if err := putEvent(ctx, tx, cp.SessionID, seq, store.EventRewind, fmt.Sprintf(`{"to":%d}`, *cp.TruncateFrom), now); err != nil {
				return err
			}
			evs = append(evs, bus.Envelope{V: bus.Version, Seq: int64(seq), At: now, Session: cp.SessionID, Kind: store.EventRewind,
				Data: map[string]any{"to": *cp.TruncateFrom}})
			seq++ // 每次截断占一个 sync_seq，订阅者据此识别回退
		}
		start := seq
		if seq, err = appendEntries(ctx, tx, cp.SessionID, cp.AppendFrom, cp.Append, seq); err != nil {
			return err
		}
		for i, e := range cp.Append {
			b, _ := json.Marshal(e)
			evs = append(evs, bus.Envelope{V: bus.Version, Seq: int64(start + i), At: e.At, Session: cp.SessionID, Kind: store.EventEntry,
				Data: map[string]any{"idx": cp.AppendFrom + i, "entry": json.RawMessage(b)}})
		}
		// 其他持久事件（档位、设置、运行状态……）排在条目之后，各占一个游标（06 10.2.1 规则 2）。
		for _, ev := range cp.Events {
			data := string(ev.Data)
			if data == "" {
				data = "{}"
			}
			if err := putEvent(ctx, tx, cp.SessionID, seq, ev.Kind, data, now); err != nil {
				return err
			}
			evs = append(evs, bus.Envelope{V: bus.Version, Seq: int64(seq), At: now, Session: cp.SessionID, Kind: ev.Kind,
				Data: json.RawMessage(data)})
			seq++
		}
		hs, hb, err := splitHeader(cp.Header)
		if err != nil {
			return err
		}
		if err := putShell(ctx, tx, hs); err != nil {
			return err
		}
		if r := cp.Request; r != nil {
			if len(r.ShellBody) > 0 {
				if err := putShell(ctx, tx, shell{hash: r.ShellHash, body: r.ShellBody}); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO session_requests(tenant, session_id, request_id, shell_hash, prefix_base, prefix_end,
				compaction_view, final_hash, composition, created_at) VALUES(?,?,?,?,?,?,?,?,?,?)
				ON CONFLICT(tenant, session_id, request_id) DO UPDATE SET shell_hash=excluded.shell_hash, prefix_base=excluded.prefix_base,
				prefix_end=excluded.prefix_end, compaction_view=excluded.compaction_view,
				final_hash=excluded.final_hash, composition=excluded.composition`,
				store.Tenant, cp.SessionID, r.RequestID, r.ShellHash, r.PrefixBase, r.PrefixEnd,
				raw(r.CompactionView), r.FinalHash, raw(r.Composition), now); err != nil {
				return err
			}
		}
		for _, id := range cp.ConsumeInbox {
			if _, err := tx.ExecContext(ctx, `UPDATE session_inbox SET consumed_at = ? WHERE tenant = ? AND session_id = ? AND item_id = ? AND consumed_at = 0`, now, store.Tenant, cp.SessionID, id); err != nil {
				return err
			}
		}
		for _, s := range cp.Spans {
			if err := putSpan(ctx, tx, cp.SessionID, s); err != nil {
				return err
			}
		}
		for _, o := range cp.Outbox {
			at := o.CreatedAt
			if at == 0 {
				at = now
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO session_outbox(tenant, id, session_id, envelope, created_at, sent)
				VALUES(?,?,?,?,?,0) ON CONFLICT(id) DO NOTHING`,
				store.Tenant, o.ID, cp.SessionID, string(o.Envelope), at); err != nil {
				return err
			}
		}
		run := cp.RunState
		if run == "" {
			run = "idle"
		}
		res, err := tx.ExecContext(ctx, `UPDATE sessions SET header = ?, shell_hash = ?, log_len = ?, next_sync_seq = ?, run_state = ?,
			preview = CASE WHEN ? = '' THEN preview ELSE ? END, updated_at = ? WHERE tenant = ? AND id = ? AND log_len = ?`,
			hb, hs.hash, cp.Header.LogLen, seq, run, cp.Preview, cp.Preview, now, store.Tenant, cp.SessionID, cp.ExpectLogLen)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return store.ErrConflict
		}
		return nil
	})
	if err == nil {
		if db.tx != nil {
			db.events = append(db.events, evs...)
		} else {
			db.notify(evs)
		}
	}
	return err
}

// Load 读头部 + 外壳，并从 base（默认 compaction_base）起读条目。
func (db *DB) Load(ctx context.Context, id string, base *int) (*store.Loaded, error) {
	var (
		l              store.Loaded
		hb, shellH     string
		key            sql.NullString
		metaJ, useCfgJ string
	)
	err := db.rq().QueryRowContext(ctx, `SELECT agent_id, agent_hash, creator, parent_id, origin, fork_upto, title, run_state, header, shell_hash,
		session_key, meta, use_config
		FROM sessions WHERE id = ? AND tenant = ? AND status != 'deleted'`, id, store.Tenant).
		Scan(&l.Meta.AgentID, &l.Meta.AgentHash, &l.Meta.Creator, &l.Meta.ParentID, &l.Meta.Origin, &l.Meta.ForkUpto, &l.Meta.Title, &l.RunState, &hb, &shellH,
			&key, &metaJ, &useCfgJ)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	l.Meta.ID = id
	l.Meta.SessionKey = key.String
	l.Meta.Meta = json.RawMessage(metaJ)
	l.Meta.UseConfig = json.RawMessage(useCfgJ)
	hdr, err := session.UnmarshalHeader([]byte(hb))
	if err != nil {
		return nil, err
	}
	var cfgBody string
	if err := db.rq().QueryRowContext(ctx, `SELECT body FROM session_shells WHERE hash = ?`, shellH).Scan(&cfgBody); err != nil {
		return nil, fmt.Errorf("外壳 %s: %w", shellH, err)
	}
	if cfgBody != "null" {
		var cfg session.Config
		if err := json.Unmarshal([]byte(cfgBody), &cfg); err != nil {
			return nil, err
		}
		hdr.Config = &cfg
	}
	from := hdr.CompactionBase
	if base != nil {
		from = *base
	}
	entries, err := db.Entries(ctx, id, from, -1)
	if err != nil {
		return nil, err
	}
	l.Snapshot = session.Snapshot{Header: hdr, Base: from, Entries: entries}
	return &l, nil
}

// Entries 按完整日志下标读 [from, to)；to < 0 表示到末尾（界面分页也用它）。
func (db *DB) Entries(ctx context.Context, id string, from, to int) ([]*session.Entry, error) {
	q := `SELECT body FROM session_entries WHERE tenant = ? AND session_id = ? AND idx >= ?`
	args := []any{store.Tenant, id, from}
	if to >= 0 {
		q += ` AND idx < ?`
		args = append(args, to)
	}
	rows, err := db.rq().QueryContext(ctx, q+` ORDER BY idx`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*session.Entry
	for rows.Next() {
		var b string
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		var e session.Entry
		if err := json.Unmarshal([]byte(b), &e); err != nil {
			return nil, err
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}

// GetRequest 读请求记录；不存在返回 (nil, nil)。
func (db *DB) GetRequest(ctx context.Context, sessionID, requestID string) (*store.RequestRecord, error) {
	var r store.RequestRecord
	var cv, comp string
	err := db.rq().QueryRowContext(ctx, `SELECT shell_hash, prefix_base, prefix_end, compaction_view, final_hash, composition
		FROM session_requests WHERE tenant = ? AND session_id = ? AND request_id = ?`, store.Tenant, sessionID, requestID).
		Scan(&r.ShellHash, &r.PrefixBase, &r.PrefixEnd, &cv, &r.FinalHash, &comp)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.RequestID = requestID
	r.CompactionView, r.Composition = unraw(cv), unraw(comp)
	return &r, nil
}

// Shell 按哈希读外壳（Config JSON）。
func (db *DB) Shell(ctx context.Context, hash string) (*session.Config, error) {
	var b string
	if err := db.rq().QueryRowContext(ctx, `SELECT body FROM session_shells WHERE hash = ?`, hash).Scan(&b); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	var c session.Config
	if err := json.Unmarshal([]byte(b), &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// ShellBody 按哈希读外壳原文（请求外壳与会话 Config 共用 session_shells，R5）。
func (db *DB) ShellBody(ctx context.Context, hash string) ([]byte, error) {
	var b string
	if err := db.rq().QueryRowContext(ctx, `SELECT body FROM session_shells WHERE hash = ?`, hash).Scan(&b); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	return []byte(b), nil
}

// Exists 报告会话是否存在（未删除）。
func (db *DB) Exists(ctx context.Context, id string) (bool, error) {
	var n int
	err := db.rq().QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE id = ? AND tenant = ? AND status != 'deleted'`, id, store.Tenant).Scan(&n)
	return n > 0, err
}

// —— 内部 ——//

type shell struct {
	hash string
	body []byte
}

// splitHeader：头部去掉 Config 后序列化；Config 作为外壳单独存（R5）。
func splitHeader(h session.Header) (shell, []byte, error) {
	body, err := json.Marshal(h.Config)
	if err != nil {
		return shell{}, nil, err
	}
	sum := sha256.Sum256(body)
	h.Config = nil
	hb, err := session.MarshalHeader(&h)
	if err != nil {
		return shell{}, nil, err
	}
	return shell{hash: hex.EncodeToString(sum[:]), body: body}, hb, nil
}

func putShell(ctx context.Context, tx *sql.Tx, s shell) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO session_shells(hash, body, created_at) VALUES(?,?,?) ON CONFLICT(hash) DO NOTHING`,
		s.hash, string(s.body), time.Now().UnixMilli())
	return err
}

func appendEntries(ctx context.Context, tx *sql.Tx, sid string, from int, es []*session.Entry, seq int) (int, error) {
	for i, e := range es {
		b, err := json.Marshal(e)
		if err != nil {
			return seq, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO session_entries(tenant, session_id, idx, id, turn, kind, summary, body, at, sync_seq)
			VALUES(?,?,?,?,?,?,?,?,?,?)`, store.Tenant, sid, from+i, e.ID, e.Turn, e.Item.Kind, Summarize(e), string(b), e.At, seq); err != nil {
			return seq, err
		}
		// 事件行只记位置与 id，正文回放时从条目表 / 归档按 id 取（06 10.2.1 规则 3）。
		d, _ := json.Marshal(map[string]any{"idx": from + i, "entry_id": e.ID})
		if err := putEvent(ctx, tx, sid, seq, store.EventEntry, string(d), e.At); err != nil {
			return seq, err
		}
		seq++
	}
	return seq, nil
}

// putEvent 追加一行会话事件（与它描述的状态变化同事务）。
func putEvent(ctx context.Context, tx *sql.Tx, sid string, cursor int, kind, data string, at int64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO session_events(tenant, session_id, cursor, kind, data, at) VALUES(?,?,?,?,?,?)`,
		store.Tenant, sid, cursor, kind, data, at)
	return err
}

func putSpan(ctx context.Context, tx *sql.Tx, sid string, s store.Span) error {
	var end int64
	if !s.End.IsZero() {
		end = s.End.UnixMilli()
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO trace_spans(tenant, session_id, span_id, parent_span, kind, name, started_at, ended_at, status, attrs)
		VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(span_id) DO UPDATE SET ended_at=excluded.ended_at, status=excluded.status, attrs=excluded.attrs`,
		store.Tenant, sid, s.ID, s.Parent, s.Kind, s.Name, s.Start.UnixMilli(), end, s.Status, raw(s.Attrs))
	return err
}

func (db *DB) inTx(ctx context.Context, f func(*sql.Tx) error) error {
	if db.tx != nil { // 已在外部事务里：加入它，由最外层提交/回滚。
		return f(db.tx)
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := f(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func raw(b json.RawMessage) string {
	if len(b) == 0 {
		return ""
	}
	return string(b)
}

func unraw(s string) json.RawMessage {
	if s == "" {
		return nil
	}
	return json.RawMessage(s)
}

// FindSessionByKey 按会话键查会话 id（13 13.3 B19）。
func (s *DB) FindSessionByKey(ctx context.Context, agentID, creator, key string) (string, error) {
	var id string
	err := s.q().QueryRowContext(ctx,
		`SELECT id FROM sessions WHERE tenant = ? AND agent_id = ? AND creator = ? AND session_key = ?`,
		store.Tenant, agentID, creator, key).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", store.ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("sqlite: 按会话键查会话: %w", err)
	}
	return id, nil
}
