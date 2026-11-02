package sqlite

import (
	"context"
	"time"

	"ybase-kernel/core/store"
)

var _ store.TraceStore = (*DB)(nil)

// RecordTrace 追加一条 trace 事件。
func (db *DB) RecordTrace(ctx context.Context, ev store.TraceEvent) error {
	if ev.At == 0 {
		ev.At = time.Now().UnixMilli()
	}
	_, err := db.q().ExecContext(ctx, `INSERT INTO trace_events(trace, at, session, kind, detail) VALUES(?,?,?,?,?)`,
		ev.Trace, ev.At, ev.Session, ev.Kind, ev.Detail)
	if err != nil {
		return err
	}
	return nil
}

// TraceEvents 按发生顺序列出某个 trace 的全部事件。
func (db *DB) TraceEvents(ctx context.Context, trace string) ([]store.TraceEvent, error) {
	rows, err := db.q().QueryContext(ctx, `SELECT trace, at, session, kind, detail FROM trace_events WHERE trace = ? ORDER BY at, rowid`, trace)
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
