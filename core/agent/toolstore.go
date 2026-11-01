package agent

// toolhub.Store 适配 store/sqlite（M4.4）。放在宿主侧，store 与 toolhub 互不依赖。

import (
	"context"
	"time"

	"ybase-kernel/core/store/sqlite"
	"ybase-kernel/core/toolhub"
)

// ToolStore 把 *sqlite.DB 包装成 toolhub.Store。
type ToolStore struct{ DB *sqlite.DB }

// Lookup 读记录。
func (s ToolStore) Lookup(ctx context.Context, sid, cid string) (toolhub.Record, bool, error) {
	r, ok, err := s.DB.LookupTool(ctx, sid, cid)
	return toolhub.Record{Status: r.Status, Output: r.Output, Decision: r.Decision, ArgsHash: r.ArgsHash}, ok, err
}

// PutToolStarted 执行前写 started（已批准的推进为 started）。
func (s ToolStore) PutToolStarted(ctx context.Context, sid, cid, h string) error {
	return s.DB.StartTool(ctx, sid, cid, h)
}

// FinishTool 写 done。
func (s ToolStore) FinishTool(ctx context.Context, sid, cid, from, out string) error {
	return s.DB.FinishTool(ctx, sid, cid, from, out)
}

// PutPending 写等确认记录。
func (s ToolStore) PutPending(ctx context.Context, sid, cid, h, reason string, dl time.Time) error {
	return s.DB.PendConfirm(ctx, sid, cid, h, reason, dl)
}
