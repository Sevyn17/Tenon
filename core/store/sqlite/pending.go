package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"ybase-kernel/core/store"
)

// IssueCorr 登记一条待回复（11 11.2 待回复表 / 13 13.3 D3）。
func (db *DB) IssueCorr(ctx context.Context, p store.Pending) error {
	if p.Corr == "" || p.SessionID == "" {
		return fmt.Errorf("sqlite: corr 登记缺 corr 或 session_id")
	}
	_, err := db.q().ExecContext(ctx, `INSERT INTO session_pending(tenant, corr, session_id, reply_to, trace, issued_idx, deadline, used, created_at)
		VALUES(?,?,?,?,?,?,?,0,?)`, Tenant, p.Corr, p.SessionID, p.ReplyTo, p.Trace, p.IssuedIdx, p.Deadline, time.Now().UnixMilli())
	return err
}

// ClaimCorr 核对并占用一条待回复：存在、未过期、未用过、发送者 = 预期回复方。
//
// 用条件 UPDATE 保证只收第一条：并发的两封回信只有一个能把 used 从 0 改成 1。
func (db *DB) ClaimCorr(ctx context.Context, corr, sessionID, from string) (store.Pending, error) {
	res, err := db.q().ExecContext(ctx, `UPDATE session_pending SET used = 1
		WHERE tenant = ? AND corr = ? AND session_id = ? AND used = 0 AND (deadline = 0 OR deadline > ?)
		AND (reply_to = '' OR reply_to = ?)`, Tenant, corr, sessionID, time.Now().UnixMilli(), from)
	if err != nil {
		return store.Pending{}, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return store.Pending{}, store.ErrCorrRejected
	}
	return db.PendingCorr(ctx, corr)
}

// PendingCorr 读一条登记（不存在返回 ErrNotFound）。
func (db *DB) PendingCorr(ctx context.Context, corr string) (store.Pending, error) {
	var p store.Pending
	err := db.q().QueryRowContext(ctx, `SELECT corr, session_id, reply_to, trace, issued_idx, deadline, used
		FROM session_pending WHERE tenant = ? AND corr = ?`, Tenant, corr).
		Scan(&p.Corr, &p.SessionID, &p.ReplyTo, &p.Trace, &p.IssuedIdx, &p.Deadline, &p.Used)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Pending{}, store.ErrNotFound
	}
	return p, err
}

// VoidCorrAfter 作废某会话在日志位置 idx 及之后签发的未用 corr（Rewind 语义，11 11.2）。
// VoidCorr 作废一条 corr；已用过或不存在时什么也不做。
func (db *DB) VoidCorr(ctx context.Context, corr string) error {
	_, err := db.q().ExecContext(ctx, `UPDATE session_pending SET used = 1 WHERE tenant = ? AND corr = ? AND used = 0`, Tenant, corr)
	return err
}

func (db *DB) VoidCorrAfter(ctx context.Context, sessionID string, idx int) (int, error) {
	res, err := db.q().ExecContext(ctx, `UPDATE session_pending SET used = 1
		WHERE tenant = ? AND session_id = ? AND issued_idx >= ? AND used = 0`, Tenant, sessionID, idx)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
