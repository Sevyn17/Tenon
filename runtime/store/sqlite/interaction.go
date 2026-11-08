package sqlite

// 交互（protocol 2，06 10.6）：session_pending 里带 interaction_id 的行。登记与结束各一个事务，
// 状态行与持久事件同事务写入（10.2.1 规则 2）；子会话的交互同时写进根会话的事件序列。

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

const pendingCols = `corr, session_id, reply_to, trace, issued_idx, deadline, used, created_at,
	interaction_id, kind, payload, root, outcome, resolved_at, resolved_by, client_id, answer`

func scanPending(row interface{ Scan(...any) error }) (store.Pending, error) {
	var p store.Pending
	var payload string
	err := row.Scan(&p.Corr, &p.SessionID, &p.ReplyTo, &p.Trace, &p.IssuedIdx, &p.Deadline, &p.Used, &p.CreatedAt,
		&p.InteractionID, &p.Kind, &payload, &p.Root, &p.Outcome, &p.ResolvedAt, &p.By, &p.ClientID, &p.Answer)
	if payload != "" {
		p.Payload = json.RawMessage(payload)
	}
	return p, err
}

// OpenInteraction 登记一次交互并写 interaction_opened 事件（发起会话；子会话的再写一份进根会话）。
func (db *DB) OpenInteraction(ctx context.Context, p store.Pending, epoch int64) error {
	if p.Corr == "" || p.SessionID == "" || p.InteractionID == "" {
		return fmt.Errorf("sqlite: 交互登记缺 corr、session_id 或 interaction_id")
	}
	now := time.Now().UnixMilli()
	var evs []bus.Envelope
	err := db.inTx(ctx, func(tx *sql.Tx) error {
		if epoch != 0 {
			var cur int64
			err := tx.QueryRowContext(ctx, `SELECT lease_epoch FROM sessions WHERE tenant = ? AND id = ?`, store.Tenant, p.SessionID).Scan(&cur)
			if errors.Is(err, sql.ErrNoRows) {
				return store.ErrNotFound
			}
			if err != nil {
				return err
			}
			if cur != epoch {
				return fmt.Errorf("%w: 期望纪元 %d，实际 %d", store.ErrLeaseLost, epoch, cur)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO session_pending(tenant, corr, session_id, reply_to, trace, issued_idx, deadline, used,
			created_at, interaction_id, kind, payload, root) VALUES(?,?,?,?,?,?,?,0,?,?,?,?,?)`,
			store.Tenant, p.Corr, p.SessionID, p.ReplyTo, p.Trace, p.IssuedIdx, p.Deadline, now,
			p.InteractionID, p.Kind, string(p.Payload), p.Root); err != nil {
			return err
		}
		data := string(p.Payload)
		if data == "" {
			data = "{}"
		}
		var err error
		evs, err = interactionEvents(ctx, tx, p, store.EventInteractionOpened, data, now)
		return err
	})
	if err == nil {
		db.emit(evs)
	}
	return err
}

// ResolveInteraction 结束一次交互：只有第一次生效，同一事务里 corr 标记已用并写 interaction_resolved 事件。
func (db *DB) ResolveInteraction(ctx context.Context, id string, r store.Resolution) (store.Pending, bool, error) {
	now := time.Now().UnixMilli()
	var (
		out store.Pending
		won bool
		evs []bus.Envelope
	)
	err := db.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE session_pending SET outcome = ?, resolved_at = ?, resolved_by = ?, client_id = ?,
			answer = ?, used = 1 WHERE tenant = ? AND interaction_id = ? AND outcome = ''`,
			r.Outcome, now, r.By, r.ClientID, r.Answer, store.Tenant, id)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		out, err = scanPending(tx.QueryRowContext(ctx, `SELECT `+pendingCols+` FROM session_pending
			WHERE tenant = ? AND interaction_id = ?`, store.Tenant, id))
		if errors.Is(err, sql.ErrNoRows) {
			return store.ErrNotFound
		}
		if err != nil || n != 1 {
			return err
		}
		won = true
		d, _ := json.Marshal(map[string]any{"id": id, "session": out.SessionID, "outcome": out.Outcome, "by": out.By})
		evs, err = interactionEvents(ctx, tx, out, store.EventInteractionResolved, string(d), now)
		return err
	})
	if err != nil {
		return store.Pending{}, false, err
	}
	db.emit(evs)
	return out, won, nil
}

// Interaction 读一次交互。
func (db *DB) Interaction(ctx context.Context, id string) (store.Pending, error) {
	p, err := scanPending(db.rq().QueryRowContext(ctx, `SELECT `+pendingCols+` FROM session_pending
		WHERE tenant = ? AND interaction_id = ?`, store.Tenant, id))
	if errors.Is(err, sql.ErrNoRows) {
		return store.Pending{}, store.ErrNotFound
	}
	return p, err
}

// OpenInteractions 未结束的交互（按登记时间）。
func (db *DB) OpenInteractions(ctx context.Context, session string) ([]store.Pending, error) {
	q := `SELECT ` + pendingCols + ` FROM session_pending WHERE tenant = ? AND interaction_id <> '' AND outcome = ''`
	args := []any{store.Tenant}
	if session != "" {
		q += ` AND (session_id = ? OR root = ?)`
		args = append(args, session, session)
	}
	rows, err := db.rq().QueryContext(ctx, q+` ORDER BY created_at, rowid`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Pending
	for rows.Next() {
		p, err := scanPending(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// interactionEvents 把一条交互事件写进发起会话的序列；有根会话（且还在）时再写一份进根会话。
func interactionEvents(ctx context.Context, tx *sql.Tx, p store.Pending, kind, data string, now int64) ([]bus.Envelope, error) {
	ids := []string{p.SessionID}
	if p.Root != "" && p.Root != p.SessionID {
		ids = append(ids, p.Root)
	}
	var evs []bus.Envelope
	for i, sid := range ids {
		var seq int64
		err := tx.QueryRowContext(ctx, `SELECT next_sync_seq FROM sessions WHERE tenant = ? AND id = ?`, store.Tenant, sid).Scan(&seq)
		if errors.Is(err, sql.ErrNoRows) {
			if i == 0 {
				return nil, store.ErrNotFound
			}
			continue // 根会话已删除：只写发起会话
		}
		if err != nil {
			return nil, err
		}
		if err := putEvent(ctx, tx, sid, int(seq), kind, data, now); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET next_sync_seq = ? WHERE tenant = ? AND id = ?`, seq+1, store.Tenant, sid); err != nil {
			return nil, err
		}
		evs = append(evs, bus.Envelope{V: bus.Version, Seq: seq, At: now, Session: sid, Kind: kind, Data: json.RawMessage(data)})
	}
	return evs, nil
}

// emit 提交后的事件通知：在外部事务里时攒到外层提交后再发（与 Commit 一致）。
func (db *DB) emit(evs []bus.Envelope) {
	if db.tx != nil {
		db.events = append(db.events, evs...)
		return
	}
	db.notify(evs)
}
