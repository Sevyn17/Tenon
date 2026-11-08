package sqlite

// 事件回放与会话列表（应用接口层，docs/90 - 归档/10 - 框架模块/12 - 应用接口层.md §三、§四）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Sevyn17/tenon/runtime/bus"
	"github.com/Sevyn17/tenon/runtime/store"
)

// OnCommit 设置提交成功后的持久事件回调（应用把它接到 bus.Publish，供 Follow 的实时部分）。
// 回调在提交的 goroutine 里同步执行，须很快返回。
func (db *DB) OnCommit(f func(bus.Envelope)) {
	db.mu.Lock()
	db.onCommit = f
	db.mu.Unlock()
}

func (db *DB) notify(evs []bus.Envelope) {
	db.mu.Lock()
	f := db.onCommit
	db.mu.Unlock()
	if f == nil {
		return
	}
	for _, e := range evs {
		f(e)
	}
}

// Replay 实现 bus.Source：只读会话事件表（06 10.2.1 规则 4），返回 cursor > after 的持久事件（升序）及水位。
// entry 事件的正文按 entry_id 从条目表或归档取回，回放形状仍是 {idx, entry}；其他事件 data 原样。
// after 早于按保留期清掉的游标（event_floor）时返回 store.ErrCursorExpired（Q3）。
func (db *DB) Replay(ctx context.Context, session string, after int64) ([]bus.Envelope, int64, error) {
	var next, floor int64
	if err := db.rq().QueryRowContext(ctx, `SELECT next_sync_seq, event_floor FROM sessions WHERE tenant = ? AND id = ?`,
		store.Tenant, session).Scan(&next, &floor); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, 0, store.ErrNotFound
		}
		return nil, 0, err
	}
	if after < floor {
		return nil, 0, fmt.Errorf("%w: 游标 %d 早于 %d", store.ErrCursorExpired, after, floor)
	}
	rows, err := db.rq().QueryContext(ctx, `SELECT e.cursor, e.kind, e.data, e.at,
		COALESCE((SELECT body FROM session_entries s WHERE s.tenant = e.tenant AND s.session_id = e.session_id
			AND s.id = json_extract(e.data, '$.entry_id')),
		(SELECT body FROM session_entries_archive a WHERE a.tenant = e.tenant AND a.session_id = e.session_id
			AND a.id = json_extract(e.data, '$.entry_id') ORDER BY a.rewind_seq LIMIT 1), '')
		FROM session_events e WHERE e.tenant = ? AND e.session_id = ? AND e.cursor > ? ORDER BY e.cursor`,
		store.Tenant, session, after)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []bus.Envelope
	for rows.Next() {
		var cur, at int64
		var kind, data, body string
		if err := rows.Scan(&cur, &kind, &data, &at, &body); err != nil {
			return nil, 0, err
		}
		out = append(out, replayEnvelope(session, cur, kind, data, body, at))
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return out, next - 1, nil
}

// replayEnvelope 一行事件 → 总线信封。entry / rewind 的 data 解成 map（与提交通知同形），其余原样。
func replayEnvelope(session string, cur int64, kind, data, body string, at int64) bus.Envelope {
	env := bus.Envelope{V: bus.Version, Seq: cur, At: at, Session: session, Kind: kind}
	switch kind {
	case store.EventEntry:
		var d struct {
			Idx int `json:"idx"`
		}
		_ = json.Unmarshal([]byte(data), &d)
		env.Data = map[string]any{"idx": d.Idx, "entry": json.RawMessage(body)}
	case store.EventRewind:
		var d struct {
			To int `json:"to"`
		}
		_ = json.Unmarshal([]byte(data), &d)
		env.Data = map[string]any{"to": d.To}
	default:
		env.Data = json.RawMessage(data)
	}
	return env
}

// ListSessions 按更新时间倒序列出未删除会话；before > 0 时只列 updated_at < before 的（分页游标）。
// topOnly 只列顶层会话（不含子智能体 / 成员）。
func (db *DB) ListSessions(ctx context.Context, before int64, limit int, topOnly bool) ([]store.SessionInfo, error) {
	return db.ListSessionsIn(ctx, "", before, limit, topOnly)
}

// ListSessionsIn 同 ListSessions，只列属于 workspace 的会话（空 = 不过滤）。
func (db *DB) ListSessionsIn(ctx context.Context, workspace string, before int64, limit int, topOnly bool) ([]store.SessionInfo, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	q := `SELECT id, title, preview, run_state, origin, parent_id, workspace, updated_at, meta FROM sessions
		WHERE tenant = ? AND status != 'deleted'`
	args := []any{store.Tenant}
	if workspace != "" {
		q += ` AND workspace = ?`
		args = append(args, workspace)
	}
	if before > 0 {
		q += ` AND updated_at < ?`
		args = append(args, before)
	}
	if topOnly {
		q += ` AND parent_id = ''`
	}
	q += ` ORDER BY updated_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := db.rq().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return scanInfos(rows)
}

// UpdateSessionMeta 改标题与业务键，同事务写 meta 事件（store.SessionMetaStore）。不改 updated_at。
func (db *DB) UpdateSessionMeta(ctx context.Context, id string, title *string, set map[string]any) error {
	now := time.Now().UnixMilli()
	var evs []bus.Envelope
	err := db.inTx(ctx, func(tx *sql.Tx) error {
		var cur, metaJ string
		var seq int
		err := tx.QueryRowContext(ctx, `SELECT title, meta, next_sync_seq FROM sessions WHERE tenant = ? AND id = ? AND status != 'deleted'`,
			store.Tenant, id).Scan(&cur, &metaJ, &seq)
		if errors.Is(err, sql.ErrNoRows) {
			return store.ErrNotFound
		}
		if err != nil {
			return err
		}
		if title != nil {
			cur = *title
		}
		meta, err := store.MergeMeta(json.RawMessage(metaJ), set)
		if err != nil {
			return err
		}
		data, _ := json.Marshal(map[string]any{"title": cur, "meta": meta})
		if err := putEvent(ctx, tx, id, seq, store.EventMeta, string(data), now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET title = ?, meta = ?, next_sync_seq = ? WHERE tenant = ? AND id = ?`,
			cur, string(meta), seq+1, store.Tenant, id); err != nil {
			return err
		}
		evs = append(evs, bus.Envelope{V: bus.Version, Seq: int64(seq), At: now, Session: id, Kind: store.EventMeta, Data: json.RawMessage(data)})
		return nil
	})
	if err == nil {
		db.emit(evs)
	}
	return err
}

// SessionsWithMeta 业务键 key 为 true 的未删除会话（store.SessionMetaStore）。
func (db *DB) SessionsWithMeta(ctx context.Context, key string, limit int) ([]store.SessionInfo, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := db.rq().QueryContext(ctx, `SELECT id, title, preview, run_state, origin, parent_id, workspace, updated_at, meta FROM sessions
		WHERE tenant = ? AND status != 'deleted' AND json_extract(meta, '$.' || ?) = 1 ORDER BY updated_at DESC LIMIT ?`,
		store.Tenant, key, limit)
	if err != nil {
		return nil, err
	}
	return scanInfos(rows)
}

func scanInfos(rows *sql.Rows) ([]store.SessionInfo, error) {
	defer rows.Close()
	var out []store.SessionInfo
	for rows.Next() {
		var s store.SessionInfo
		var meta string
		if err := rows.Scan(&s.ID, &s.Title, &s.Preview, &s.RunState, &s.Origin, &s.ParentID, &s.Workspace, &s.UpdatedAt, &meta); err != nil {
			return nil, err
		}
		if meta != "" && meta != "{}" {
			s.Meta = json.RawMessage(meta)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

var _ store.SessionMetaStore = (*DB)(nil)
