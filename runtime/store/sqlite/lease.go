package sqlite

// 会话写租约（02 2.2 fencing）：sessions.lease_holder / lease_epoch 两列在 T1a 已建。
//
// 抢租约 = 持有者改名 + 纪元加一；写入带纪元，旧节点丢租约后的迟到写入被 Commit 拒绝。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"ybase/runtime/store"
)

// AcquireLease 抢租约并返回新纪元（从 1 起）。会话不存在返回 ErrNotFound。
func (db *DB) AcquireLease(ctx context.Context, sessionID, holder string) (int64, error) {
	if holder == "" {
		return 0, fmt.Errorf("%w: 租约持有者不能为空", store.ErrLeaseLost)
	}
	var epoch int64
	err := db.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE sessions SET lease_holder = ?, lease_epoch = lease_epoch + 1
			WHERE tenant = ? AND id = ? AND status != 'deleted'`, holder, store.Tenant, sessionID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return store.ErrNotFound
		}
		err = tx.QueryRowContext(ctx, `SELECT lease_epoch FROM sessions WHERE tenant = ? AND id = ?`, store.Tenant, sessionID).Scan(&epoch)
		if errors.Is(err, sql.ErrNoRows) {
			return store.ErrNotFound
		}
		return err
	})
	return epoch, err
}

// RenewLease 续租：holder 与 epoch 都匹配才成功，否则 ErrLeaseLost。
func (db *DB) RenewLease(ctx context.Context, sessionID, holder string, epoch int64) error {
	if holder == "" || epoch <= 0 {
		return store.ErrLeaseLost
	}
	return db.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE sessions SET lease_holder = ?
			WHERE tenant = ? AND id = ? AND lease_holder = ? AND lease_epoch = ? AND status != 'deleted'`,
			holder, store.Tenant, sessionID, holder, epoch)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return fmt.Errorf("%w: 会话 %s 纪元 %d 不再由 %s 持有", store.ErrLeaseLost, sessionID, epoch, holder)
		}
		return nil
	})
}

// ReleaseLease 释放租约：holder/epoch 匹配才清空持有者；不匹配忽略（幂等释放）。
func (db *DB) ReleaseLease(ctx context.Context, sessionID, holder string, epoch int64) error {
	if holder == "" || epoch <= 0 {
		return nil
	}
	return db.inTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE sessions SET lease_holder = ''
			WHERE tenant = ? AND id = ? AND lease_holder = ? AND lease_epoch = ?`, store.Tenant, sessionID, holder, epoch)
		return err
	})
}
