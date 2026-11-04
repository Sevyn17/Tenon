package sqlite

// session_outbox（08 9.6）：外发与检查点同事务写入，提交后由 Host 派发并标记 sent；
// 崩溃后未发的行仍会被重试。

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"ybase/runtime/store"
)

// Outbox 返回待外发的信封。
func (db *DB) Outbox(ctx context.Context, limit int) ([]store.OutboxItem, error) {
	q := `SELECT id, session_id, envelope, created_at FROM session_outbox WHERE tenant = ? AND sent = 0 AND dead = '' ORDER BY created_at, id`
	args := []any{store.Tenant}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := db.rq().QueryContext(ctx, q, args...)
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
	_, err := db.q().ExecContext(ctx, `UPDATE session_outbox SET sent = 1, claimed_by = '', claim_until = 0 WHERE tenant = ? AND id = ?`, store.Tenant, id)
	return err
}

// ClaimOutbox 原子认领待派发信封：一条 UPDATE … RETURNING 在写锁下完成挑选与标记，
// 同进程多个派发者、多个进程共用一个库都不会拿到同一条。
func (db *DB) ClaimOutbox(ctx context.Context, holder string, lease time.Duration, limit int) ([]store.OutboxItem, error) {
	if limit <= 0 {
		limit = -1
	}
	now := time.Now().UnixMilli()
	rows, err := db.q().QueryContext(ctx, `UPDATE session_outbox SET claimed_by = ?, claim_until = ?, attempts = attempts + 1
		WHERE tenant = ? AND id IN (
			SELECT id FROM session_outbox WHERE tenant = ? AND sent = 0 AND dead = '' AND (claimed_by = '' OR claim_until <= ?)
			ORDER BY created_at, id LIMIT ?)
		RETURNING id, session_id, envelope, created_at, attempts`,
		holder, now+lease.Milliseconds(), store.Tenant, store.Tenant, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.OutboxItem
	for rows.Next() {
		var it store.OutboxItem
		var body string
		if err := rows.Scan(&it.ID, &it.SessionID, &body, &it.CreatedAt, &it.Attempts); err != nil {
			return nil, err
		}
		it.Envelope = json.RawMessage(body)
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { // RETURNING 不保证顺序
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt < out[j].CreatedAt
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// ReleaseOutbox 交还一条认领（只有认领者本人能交还）。
func (db *DB) ReleaseOutbox(ctx context.Context, id, holder string) error {
	_, err := db.q().ExecContext(ctx, `UPDATE session_outbox SET claimed_by = '', claim_until = 0
		WHERE tenant = ? AND id = ? AND claimed_by = ?`, store.Tenant, id, holder)
	return err
}

// DeadOutbox 把一条信封标为死信。
func (db *DB) DeadOutbox(ctx context.Context, id, reason string) error {
	if reason == "" {
		reason = "dead"
	}
	_, err := db.q().ExecContext(ctx, `UPDATE session_outbox SET dead = ?, claimed_by = '', claim_until = 0
		WHERE tenant = ? AND id = ?`, reason, store.Tenant, id)
	return err
}
