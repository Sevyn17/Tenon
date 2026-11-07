package sqlite

import (
	"context"
	"database/sql"
	"time"

	"tenon/runtime/store"
)

var _ store.TraceStore = (*DB)(nil)

// RecordTrace 追加一条 trace 事件。
func (db *DB) RecordTrace(ctx context.Context, ev store.TraceEvent) error {
	if ev.At == 0 {
		ev.At = time.Now().UnixMilli()
	}
	_, err := db.q().ExecContext(ctx, `INSERT INTO trace_events(tenant, trace, at, session, kind, detail) VALUES(?,?,?,?,?,?)`,
		store.Tenant, ev.Trace, ev.At, ev.Session, ev.Kind, ev.Detail)
	if err != nil {
		return err
	}
	return nil
}

// TraceEvents 按发生顺序列出某个 trace 的全部事件。
func (db *DB) TraceEvents(ctx context.Context, trace string) ([]store.TraceEvent, error) {
	rows, err := db.rq().QueryContext(ctx, `SELECT trace, at, session, kind, detail FROM trace_events WHERE tenant = ? AND trace = ? ORDER BY at, rowid`, store.Tenant, trace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.TraceEvent{}
	for rows.Next() {
		var ev store.TraceEvent
		if err := rows.Scan(&ev.Trace, &ev.At, &ev.Session, &ev.Kind, &ev.Detail); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// Prune 过期数据清理（见 store.PruneStore）：已用或已过期的待回复、已派发或死信的外发、trace 事件，
// 只删 created_at / at 早于 before 的行。一个事务。
func (db *DB) Prune(ctx context.Context, before time.Time) (int, error) {
	cut := before.UnixMilli()
	n := 0
	err := db.inTx(ctx, func(tx *sql.Tx) error {
		// 会话事件按保留期清理（06 10.12 Q3）：先把每个会话要删的最大游标记进 event_floor，
		// 早于它的订阅返回 ErrCursorExpired，客户端重新取快照。
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET event_floor = MAX(event_floor,
			(SELECT MAX(cursor) FROM session_events e WHERE e.tenant = sessions.tenant AND e.session_id = sessions.id AND e.at < ?))
			WHERE tenant = ? AND EXISTS (SELECT 1 FROM session_events e WHERE e.tenant = sessions.tenant AND e.session_id = sessions.id AND e.at < ?)`,
			cut, store.Tenant, cut); err != nil {
			return err
		}
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{`DELETE FROM session_pending WHERE tenant = ? AND (used = 1 OR (deadline > 0 AND deadline < ?)) AND created_at < ?`, []any{store.Tenant, cut, cut}},
			{`DELETE FROM session_outbox WHERE tenant = ? AND (sent = 1 OR dead <> '') AND created_at < ?`, []any{store.Tenant, cut}},
			{`DELETE FROM trace_events WHERE tenant = ? AND at < ?`, []any{store.Tenant, cut}},
			{`DELETE FROM session_events WHERE tenant = ? AND at < ?`, []any{store.Tenant, cut}},
		} {
			res, err := tx.ExecContext(ctx, q.sql, q.args...)
			if err != nil {
				return err
			}
			m, _ := res.RowsAffected()
			n += int(m)
		}
		return nil
	})
	return n, err
}
