package sqlite

// session_outbox（08 9.6）：外发与检查点同事务写入，提交后由 Host 派发并标记 sent；
// 崩溃后未发的行仍会被重试。

import (
	"context"
	"encoding/json"

	"ybase-kernel/core/store"
)

// Outbox 返回待外发的信封。
func (db *DB) Outbox(ctx context.Context, limit int) ([]store.OutboxItem, error) {
	q := `SELECT id, session_id, envelope, created_at FROM session_outbox WHERE tenant = ? AND sent = 0 ORDER BY created_at, id`
	args := []any{Tenant}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := db.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.OutboxItem
	for rows.Next() {
		var it store.OutboxItem
		var body string
		if err := rows.Scan(&it.ID, &it.SessionID, &body, &it.CreatedAt); err != nil {
			return nil, err
		}
		it.Envelope = json.RawMessage(body)
		out = append(out, it)
	}
	return out, rows.Err()
}

// MarkOutbox 标记一条外发信封已派发。
func (db *DB) MarkOutbox(ctx context.Context, id string) error {
	_, err := db.sql.ExecContext(ctx, `UPDATE session_outbox SET sent = 1 WHERE tenant = ? AND id = ?`, Tenant, id)
	return err
}
