package agent

// 上下文注入（框架设计 12，T2b）：调模型前收集状态提交，按 key 只发变化，
// 渲染成 status 条目追加在本次请求末尾；压缩后另写一条 L2 快照条目并重置哈希。
// 写什么由 inject.Plan 算（纯函数，审查 P1-59），这里只收集和写入。

import "ybase/runtime/inject"

// injectStatuses 收集并写入注入条目。reset=true 用于压缩之后（见 inject.Plan）。
// 返回是否追加了条目（调用方据此重建请求）。
func (a *Agent) injectStatuses(reset bool) (bool, error) {
	if len(a.Statuses) == 0 || a.Core == nil {
		return false, nil
	}
	ws := inject.Plan(inject.Collect(a.Ctx, a.Statuses), a.Core.Context().Items, reset)
	for _, w := range ws {
		if _, err := a.Core.Inject(w.Kind, w.Text, w.Meta); err != nil {
			return false, err
		}
	}
	return len(ws) > 0, nil
}
