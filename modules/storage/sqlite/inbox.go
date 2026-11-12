package sqlite

// session_inbox（session-host D2 / D6，data-contract §4.3）：
// Send / Note 先落 inbox 再确认；item_id 由调用方生成，同 id 重放幂等（首次准入胜出）。
// 被消费的项在 checkpoint 事务里标记 consumed_at（Checkpoint.ConsumeInbox），行保留，重放同 id 仍被拒。

import (
	"context"
	"github.com/Sevyn17/tenon/runtime/store"
	"time"
)

// Admit 准入一条输入。返回 true 表示新写入；false 表示同 id 已存在（重放，忽略）。
func (db *DB) Admit(ctx context.Context, sessionID string, it store.InboxItem) (bool, error) {
	if it.Trust == "" {
		it.Trust = "trusted"
	}
	res, err := db.q().ExecContext(ctx, `INSERT INTO session_inbox(tenant, session_id, item_id, kind, input, trust, envelope, created_at)
		VALUES(?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`, store.Tenant, sessionID, it.ItemID, it.Kind, it.Input, it.Trust, it.Envelope, time.Now().UnixMilli())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// Inbox 按准入顺序列出未消费项。
func (db *DB) Inbox(ctx context.Context, sessionID string) ([]store.InboxItem, error) {
	rows, err := db.rq().QueryContext(ctx, `SELECT item_id, kind, input, trust, envelope, created_at FROM session_inbox
		WHERE tenant = ? AND session_id = ? AND consumed_at = 0 ORDER BY created_at, rowid`, store.Tenant, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.InboxItem
	for rows.Next() {
		var it store.InboxItem
		var at int64
		if err := rows.Scan(&it.ItemID, &it.Kind, &it.Input, &it.Trust, &it.Envelope, &at); err != nil {
			return nil, err
		}
		it.At = time.UnixMilli(at)
		out = append(out, it)
	}
	return out, rows.Err()
}

// InInbox 报告某 item 是否仍待处理（未消费）。
func (db *DB) InInbox(ctx context.Context, sessionID, itemID string) (bool, error) {
	var n int
	err := db.rq().QueryRowContext(ctx, `SELECT COUNT(*) FROM session_inbox WHERE tenant = ? AND session_id = ? AND item_id = ? AND consumed_at = 0`,
		store.Tenant, sessionID, itemID).Scan(&n)
	return n > 0, err
}
