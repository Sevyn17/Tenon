package sqlite

// ToolResultStore 实现（tool-hub D4，data-contract §4.2）。
//
// 状态：started → done；pending → delivered → done。状态推进一律单行条件更新
// （WHERE status = 期望值），并发或重放时后到者失败而不是覆盖。

import (
	"context"
	"database/sql"
	"errors"
	"tenon/runtime/store"
	"time"
)

// PutToolStarted 有副作用的工具执行前写 started。已存在返回 ErrStateMismatch（不覆盖）。
func (db *DB) PutToolStarted(ctx context.Context, sessionID, callID, argsHash string) error {
	return db.insertTool(ctx, store.ToolRecord{SessionID: sessionID, CallID: callID, Status: store.ToolStarted, ArgsHash: argsHash})
}

// PutToolPending 写挂起记录（等回传 / 等确认 / 等回答）。
func (db *DB) PutToolPending(ctx context.Context, r store.ToolRecord) error {
	r.Status = store.ToolPending
	return db.insertTool(ctx, r)
}

func (db *DB) insertTool(ctx context.Context, r store.ToolRecord) error {
	res, err := db.q().ExecContext(ctx, `INSERT INTO tool_results(tenant, session_id, call_id, status, kind, args_hash,
		deadline, token_hash, created_at) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`,
		store.Tenant, r.SessionID, r.CallID, r.Status, r.Kind, r.ArgsHash, unixMilli(r.Deadline), r.TokenHash, time.Now().UnixMilli())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return store.ErrStateMismatch
	}
	return nil
}

// Deliver 挂起结果送达：pending → delivered（结果或决定先落库，再通知会话）。
func (db *DB) Deliver(ctx context.Context, sessionID, callID, delivered, decision string) error {
	return db.advanceTool(ctx, sessionID, callID, store.ToolPending,
		`status = ?, delivered = ?, decision = ?`, store.ToolDelivered, delivered, decision)
}

// FinishTool 写 done。from 为期望的当前状态（started 或 delivered）；
// 只读工具不写 started，可传 from = "" 直接插入 done。
func (db *DB) FinishTool(ctx context.Context, sessionID, callID, from, output string) error {
	if from == "" {
		res, err := db.q().ExecContext(ctx, `INSERT INTO tool_results(tenant, session_id, call_id, status, output, created_at)
			VALUES(?,?,?,?,?,?) ON CONFLICT DO NOTHING`, store.Tenant, sessionID, callID, store.ToolDone, output, time.Now().UnixMilli())
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return store.ErrStateMismatch
		}
		return nil
	}
	return db.advanceTool(ctx, sessionID, callID, from, `status = ?, output = ?`, store.ToolDone, output)
}

func (db *DB) advanceTool(ctx context.Context, sessionID, callID, from, set string, args ...any) error {
	args = append(args, store.Tenant, sessionID, callID, from)
	res, err := db.q().ExecContext(ctx, `UPDATE tool_results SET `+set+
		` WHERE tenant = ? AND session_id = ? AND call_id = ? AND status = ?`, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return store.ErrStateMismatch
	}
	return nil
}

// GetTool 读一条记录；不存在返回 (nil, nil)。
func (db *DB) GetTool(ctx context.Context, sessionID, callID string) (*store.ToolRecord, error) {
	var r store.ToolRecord
	var dl int64
	err := db.rq().QueryRowContext(ctx, `SELECT session_id, call_id, status, kind, args_hash, decision, delivered, output,
		deadline, token_hash FROM tool_results WHERE tenant = ? AND session_id = ? AND call_id = ?`, store.Tenant, sessionID, callID).
		Scan(&r.SessionID, &r.CallID, &r.Status, &r.Kind, &r.ArgsHash, &r.Decision, &r.Delivered, &r.Output, &dl, &r.TokenHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if dl > 0 {
		r.Deadline = time.UnixMilli(dl)
	}
	return &r, nil
}

// PendingTools 列出会话的挂起记录（未加载会话的视图、超时扫描用）。
func (db *DB) PendingTools(ctx context.Context, sessionID string) ([]store.ToolRecord, error) {
	rows, err := db.rq().QueryContext(ctx, `SELECT call_id, kind, args_hash, deadline FROM tool_results
		WHERE tenant = ? AND session_id = ? AND status = ? ORDER BY created_at`, store.Tenant, sessionID, store.ToolPending)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.ToolRecord
	for rows.Next() {
		r := store.ToolRecord{SessionID: sessionID, Status: store.ToolPending}
		var dl int64
		if err := rows.Scan(&r.CallID, &r.Kind, &r.ArgsHash, &dl); err != nil {
			return nil, err
		}
		if dl > 0 {
			r.Deadline = time.UnixMilli(dl)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteFinishedTools 回合结束后清理 done / started 记录（pending 只按 deadline 处理，data-contract §6）。
func (db *DB) DeleteFinishedTools(ctx context.Context, sessionID string) error {
	_, err := db.q().ExecContext(ctx, `DELETE FROM tool_results WHERE tenant = ? AND session_id = ? AND status IN (?, ?)`,
		store.Tenant, sessionID, store.ToolDone, store.ToolStarted)
	return err
}

func unixMilli(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}
