package agent

// 主循环接 toolhub（M3 遗留，M4.4 接入）：一步的全部调用交给 toolhub 并行执行，
// 结果按调用顺序逐个提交内核；人工确认走挂起记录（Await 先落库决定，再返回）。

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"ybase-kernel/core/session"
	"ybase-kernel/core/store"
	"ybase-kernel/core/toolhub"
)

// Ask 向人询问是否批准一次调用（CLI 实现）。
type Ask func(call *toolhub.Cx, reason string) bool

// AwaitFunc 构造 toolhub.Await：询问 → 决定落库（pending → delivered）→ 返回。
// 等待期间进程被杀，记录保持 pending，恢复后同一调用重新询问（session-host D3）。
func (a *Agent) AwaitFunc(ask Ask) toolhub.Await {
	return func(ctx context.Context, cx *toolhub.Cx, reason string, _ time.Time) (bool, error) {
		if a.Hooks.OnConfirm != nil {
			a.Hooks.OnConfirm(cx.CallID, cx.Tool.Spec.Name, reason)
		}
		if a.crashAt == "awaiting_confirm" {
			return false, errCrash
		}
		type ans struct{ ok bool }
		ch := make(chan ans, 1)
		go func() { ch <- ans{ask(cx, reason)} }()
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case r := <-ch:
			if a.Store != nil {
				if err := a.Store.Decide(ctx, cx.SessionID, cx.CallID, r.ok); err != nil {
					return false, err
				}
			}
			return r.ok, nil
		}
	}
}

// HubSpan toolhub 的每次调用观测 → trace span（G3），随下一次 checkpoint 提交。
func (a *Agent) HubSpan(ev toolhub.SpanEvent) {
	attrs := map[string]any{"call_id": ev.CallID, "risk": ev.Risk, "result_bytes": ev.ResultBytes}
	if ev.Guard != "" {
		attrs["guard"] = ev.Guard
	}
	if ev.Artifact != "" {
		attrs["artifact"] = ev.Artifact
	}
	if ev.Recovered {
		attrs["recovered"] = true
	}
	if ev.ChildSessionID != "" {
		attrs["child_session_id"] = ev.ChildSessionID // 委派：子会话可从 span 跳转（预留，M6 子智能体填）
	}
	b, _ := json.Marshal(attrs)
	parent := a.stepID
	if i := strings.LastIndex(ev.CallID, "#"); i > 0 { // 子调用（如 run_code 的 <父>#<n>）挂在父调用 span 下
		parent = "tool-" + ev.CallID[:i]
	}
	a.spanMu.Lock()
	a.hubSpans = append(a.hubSpans, store.Span{ID: "tool-" + ev.CallID, Parent: parent, Kind: "tool", Name: ev.Tool,
		Status: ev.Status, Start: ev.Start, End: ev.End, Attrs: b})
	a.spanMu.Unlock()
}

// runToolsHub 一步的工具：并行执行、按序提交。
func (a *Agent) runToolsHub(ctx context.Context, tn *session.ToolsNeed) (*session.Need, error) {
	for _, c := range tn.Calls {
		if a.Hooks.OnToolCall != nil {
			a.Hooks.OnToolCall(c)
		}
	}
	run := a.Hub.ExecuteIn(ctx, a.Toolset, a.toolCtx(), tn.Calls, tn.Hints)
	// 先收齐结果（toolhub 内部并行、按调用顺序产出），再在等待态暂存显露集追加，
	// 最后按序提交：最后一个结果触发步边界，暂存的 AddTools 在下一步请求里生效（tool-hub D1）。
	var results []toolhub.Result
	for {
		res, ok := run.Next()
		if !ok {
			break
		}
		if res.Err != nil {
			run.Stop()
			return nil, res.Err
		}
		if a.Hooks.OnToolResult != nil {
			a.Hooks.OnToolResult(res.Call, contentText(res.Output.Content), res.Output.Status.State != "" && res.Output.Status != session.ToolOK)
		}
		results = append(results, res)
	}
	if a.Toolset != nil {
		if add := a.Toolset.TakePending(); len(add) > 0 {
			if _, _, err := a.Core.SetConfig(session.ConfigPatch{AddTools: add}); err != nil {
				return nil, err
			}
		}
	}
	var need *session.Need
	for _, res := range results {
		st := res.Output.Status
		if st.State == "" {
			st = session.ToolOK
		}
		var err error
		if _, need, err = a.Core.SubmitResult(res.Call.ID, res.Output.Content, st); err != nil {
			return nil, err
		}
	}
	return need, nil
}

func contentText(c session.Content) string {
	s := ""
	for _, p := range c {
		if p.Kind == "text" {
			s += p.Text
		}
	}
	return s
}
