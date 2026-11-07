package agent

// toolhub.Store 适配 store/sqlite（M4.4）。放在宿主侧，store 与 toolhub 互不依赖。

import (
	"context"
	"time"

	"tenon/runtime/store"
	"tenon/runtime/toolhub"
)

// ToolStore 把 store.ToolResultStore 包装成 toolhub.Store。
type ToolStore struct{ DB store.ToolResultStore }

// Lookup 读记录。
func (s ToolStore) Lookup(ctx context.Context, sid, cid string) (toolhub.Record, bool, error) {
	r, err := s.DB.GetTool(ctx, sid, cid)
	if err != nil || r == nil {
		return toolhub.Record{}, false, err
	}
	return toolhub.Record{Status: r.Status, Output: r.Output, Decision: r.Decision, ArgsHash: r.ArgsHash}, true, nil
}

// PutToolStarted 执行前写 started（已批准的推进为 started）。
func (s ToolStore) PutToolStarted(ctx context.Context, sid, cid, h string) error {
	return s.DB.StartTool(ctx, sid, cid, h)
}

// FinishTool 写 done。
func (s ToolStore) FinishTool(ctx context.Context, sid, cid, from, out string) error {
	return s.DB.FinishTool(ctx, sid, cid, from, out)
}

// PutPending 写等确认记录。确认理由只随 human_requested 事件给界面，不落库（审查 P1-54）。
func (s ToolStore) PutPending(ctx context.Context, sid, cid, h, reason string, dl time.Time) error {
	return s.DB.PendConfirm(ctx, sid, cid, h, dl)
}

// Decide 人工决定送达：pending → delivered（approve / deny）。
func (s ToolStore) Decide(ctx context.Context, sid, cid string, approve bool) error {
	return Decide(ctx, s.DB, sid, cid, approve)
}

// Decide 把人工决定写进工具记录（pending → delivered）。
func Decide(ctx context.Context, db store.ToolResultStore, sid, cid string, approve bool) error {
	d := "deny"
	if approve {
		d = "approve"
	}
	return db.Deliver(ctx, sid, cid, "", d)
}
