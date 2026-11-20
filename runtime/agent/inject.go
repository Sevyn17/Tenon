package agent

// 上下文注入（框架设计 12，T2b）：调模型前收集状态提交，按 key 只发变化，
// 渲染成 status 条目追加在本次请求末尾；压缩后另写一条 L2 快照条目并重置哈希。
// 写什么由 inject.Plan 算（纯函数，审查 P1-59），这里只收集和写入。

import (
	"github.com/Sevyn17/tenon/runtime/inject"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/workmode"
)

// injectStatuses 收集并写入注入条目。reset=true 用于压缩之后（见 inject.Plan）。
// 返回是否追加了条目（调用方据此重建请求）。
//
// Plan 模式（docs/50 5.2）的指令也走这里：是一条内核自己的状态片段，进入 Plan 模式时发、退出时按 key
// 写「已清除」。不改 system，前缀缓存不失效。
func (a *Agent) injectStatuses(reset bool) (bool, error) {
	if a.Core == nil {
		return false, nil
	}
	items := inject.Collect(a.Ctx, a.Statuses)
	if a.workMode() == workmode.Plan {
		items = append(items, inject.Item{Key: workmode.StatusKey, Kind: inject.KindStatus, Body: workmode.Instruction,
			Priority: 100, Source: "kernel", Trust: inject.TrustInternal})
	}
	if len(items) == 0 && len(a.Statuses) == 0 && !a.planSeen() {
		return false, nil // 没有任何状态提交、也从没进过 Plan 模式：不必扫历史
	}
	ws := inject.Plan(items, a.Core.Context().Items, reset)
	for _, w := range ws {
		if err := a.Core.Inject(w.Kind, w.Text, w.Meta); err != nil {
			return false, err
		}
	}
	return len(ws) > 0, nil
}

// workMode 会话当前的工作方式（execute / plan）。
func (a *Agent) workMode() string {
	if a.Core == nil {
		return workmode.Execute
	}
	return workmode.Of(a.Core.Context().Ext)
}

// planSeen 本会话的可见历史里是否发过 Plan 模式指令（退出时要写一条「已清除」）。
func (a *Agent) planSeen() bool {
	_, ok := inject.HashesFrom(a.Core.Context().Items)[workmode.StatusKey]
	return ok
}

// planTools 按工作方式收窄本次请求的工具（docs/50 5.2 工具显露）：Plan 模式去掉会改动的工具；
// exit_plan 只在 Plan 模式下显露。只收窄、不改定义（reqlog R2 允许）。
func (a *Agent) planTools(req session.Request) session.Request {
	if a.Hub == nil || len(req.Tools) == 0 {
		return req
	}
	plan := a.workMode() == workmode.Plan
	out := make([]session.ToolSpec, 0, len(req.Tools))
	for _, s := range req.Tools {
		keep := true
		switch {
		case s.Name == workmode.ExitPlanTool:
			keep = plan
		case plan:
			t, ok := a.Hub.Get(s.Name)
			keep = ok && workmode.Allowed(t, a.Hub.ItemsOf(t))
		}
		if keep {
			out = append(out, s)
		}
	}
	if len(out) == len(req.Tools) {
		return req
	}
	if len(out) == 0 {
		out = nil
	}
	req.Tools = out
	return req
}
