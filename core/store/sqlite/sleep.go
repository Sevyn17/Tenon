package sqlite

// 会话休眠状态（02 2.2）：空闲会话释放进程内对象与租约，只剩存储里的几行；
// 新信封到达时任一节点抢到租约即可唤醒（从检查点恢复）。
//
// 与租约的配合：休眠在自己持租约时把 active 改成 sleeping（条件迁移带纪元），
// 唤醒在抢到租约后把 sleeping 改回 active。旧节点迟到的迁移因状态/纪元不符被拒。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"ybase-kernel/core/store"
)

// SessionStatus 读会话生命周期状态（active / sleeping / deleted）。
func (db *DB) SessionStatus(ctx context.Context, sessionID string) (string, error) {
	var st string
	err := db.q().QueryRowContext(ctx, `SELECT status FROM sessions WHERE tenant = ? AND id = ?`, Tenant, sessionID).Scan(&st)
	if errors.Is(err, sql.ErrNoRows) {
		return "", store.ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return st, nil
}

// SetSessionStatus 条件迁移：当前状态必须等于 from 才改为 to；epoch 非零时还要求持有该纪元的租约。
func (db *DB) SetSessionStatus(ctx context.Context, sessionID, from, to string, epoch int64) error {
	q := `UPDATE sessions SET status = ?, updated_at = ? WHERE tenant = ? AND id = ? AND status = ?`
	args := []any{to, time.Now().UnixMilli(), Tenant, sessionID, from}
	if epoch != 0 {
		q += ` AND lease_epoch = ?`
		args = append(args, epoch)
	}
	res, err := db.q().ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("%w: 会话 %s 不是 %s 状态（或纪元不符）", store.ErrStateMismatch, sessionID, from)
	}
	return nil
}

// SessionsWithStatus 列出某状态的会话 id（按 id 升序，limit <= 0 表示不限）。
func (db *DB) SessionsWithStatus(ctx context.Context, status string, limit int) ([]string, error) {
	q := `SELECT id FROM sessions WHERE tenant = ? AND status = ? ORDER BY id`
	args := []any{Tenant, status}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := db.q().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
