package sqlite

// 删除会话（data-contract §4.3 / D2）：先置墓碑（列表与读取立即不可见），
// 再同步调用插件登记的清理回调（OnSessionDelete），最后清理核心表并删墓碑。
// 任一回调失败：墓碑保留、返回错误，下次可重试；不会出现"插件数据残留但会话已消失"。

import (
	"context"
	"database/sql"
	"fmt"
	"time"
	"ybase/runtime/store"
)

// DeleteSession 删除会话及其全部数据。cleaners 为各插件登记的回调，按给定顺序调用。
func (db *DB) DeleteSession(ctx context.Context, id string, cleaners []store.Cleaner) error {
	res, err := db.q().ExecContext(ctx, `UPDATE sessions SET status = 'deleted', deleted_at = ? WHERE tenant = ? AND id = ?`,
		time.Now().UnixMilli(), store.Tenant, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return store.ErrNotFound
	}
	for i, c := range cleaners {
		if err := c(ctx, id); err != nil {
			return fmt.Errorf("清理回调 %d: %w（墓碑保留，可重试）", i, err)
		}
	}
	return db.inTx(ctx, func(tx *sql.Tx) error {
		for _, t := range []string{"session_entries", "session_entries_archive", "session_requests", "session_inbox",
			"tool_results", "trace_spans", "blob_refs"} {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+t+` WHERE tenant = ? AND session_id = ?`, store.Tenant, id); err != nil {
				return err
			}
		}
		// v4 起的表：待回复、发件箱按 session_id；trace 事件按 session（无租户列，见 v8 迁移）。
		for _, q := range []string{
			`DELETE FROM session_pending WHERE tenant = ? AND session_id = ?`,
			`DELETE FROM session_outbox WHERE tenant = ? AND session_id = ?`,
			`DELETE FROM trace_events WHERE tenant = ? AND session = ?`,
		} {
			if _, err := tx.ExecContext(ctx, q, store.Tenant, id); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE tenant = ? AND id = ?`, store.Tenant, id)
		return err
	})
}
