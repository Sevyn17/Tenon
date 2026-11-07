package sqlite

// toolhub.Store 适配（M4.4）：幂等记录 + 挂起确认记录。
//
// 状态：Write/Dangerous 无确认：started → done；需确认：pending → delivered(approve) → started → done；
// 拒绝 / 截止：pending|delivered → done。只读工具直接 done。

import (
	"context"
	"tenon/runtime/store"
	"time"
)

// StartTool 执行前写 started：无记录则插入；已批准（delivered）则推进为 started。
func (db *DB) StartTool(ctx context.Context, sessionID, callID, argsHash string) error {
	r, err := db.GetTool(ctx, sessionID, callID)
	if err != nil {
		return err
	}
	if r == nil {
		return db.PutToolStarted(ctx, sessionID, callID, argsHash)
	}
	if r.Status != store.ToolDelivered || r.ArgsHash != argsHash {
		return store.ErrStateMismatch
	}
	return db.advanceTool(ctx, sessionID, callID, store.ToolDelivered, `status = ?`, store.ToolStarted)
}

// PendConfirm 写等确认记录；已存在（恢复后重问同一调用）不报错。
// 仍是 pending 的记录按本次参数刷新哈希与截止：这次批准的是本次展示的参数，
// 旧进程算出的哈希（如旧版哈希规则）不应让批准后的推进失败。
func (db *DB) PendConfirm(ctx context.Context, sessionID, callID, argsHash string, deadline time.Time) error {
	_, err := db.q().ExecContext(ctx, `INSERT INTO tool_results(tenant, session_id, call_id, status, kind, args_hash,
		decision, deadline, created_at) VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(tenant, session_id, call_id) DO UPDATE SET args_hash = excluded.args_hash, deadline = excluded.deadline
		WHERE tool_results.status = 'pending'`,
		store.Tenant, sessionID, callID, store.ToolPending, "confirm", argsHash, "", unixMilli(deadline), time.Now().UnixMilli())
	return err
}
