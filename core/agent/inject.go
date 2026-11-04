package agent

// 上下文注入（框架设计 12，T2b）：调模型前收集状态提交，按 key 只发变化，
// 渲染成 status 条目追加在本次请求末尾；压缩后另写一条 L2 快照条目并重置哈希。

import (
	"ybase-kernel/core/inject"
	"ybase-kernel/core/session"
)

// injectStatuses 收集并写入注入条目。reset=true 用于压缩之后：忽略已发哈希，
// 把当前快照与状态按 key 重新注入一次（12.5）。返回是否追加了条目（调用方据此重建请求）。
func (a *Agent) injectStatuses(reset bool) (bool, error) {
	if len(a.Statuses) == 0 || a.Core == nil {
		return false, nil
	}
	ctx := a.Core.Context()
	prev := map[string]string{}
	if !reset {
		prev = inject.HashesFrom(ctx.Items)
	}
	items := inject.Collect(a.Ctx, a.Statuses)
	var snaps, stats []inject.Item
	for _, it := range items {
		switch it.Kind {
		case inject.KindSnapshot:
			snaps = append(snaps, it)
		default:
			stats = append(stats, it)
		}
	}
	effective := make(map[string]string, len(prev))
	for k, v := range prev {
		effective[k] = v
	}
	injected := false

	// L2 快照：会话开头与压缩后注入一次，不占状态更新的总预算（12.5）。
	var sentSnaps []inject.Item
	if reset {
		sentSnaps = inject.Fit(snaps)
	} else {
		sentSnaps = inject.Changes(snaps, prev)
	}
	if len(sentSnaps) > 0 {
		hashes := inject.Hashes(sentSnaps)
		for k, v := range hashes {
			effective[k] = v
		}
		meta := map[string]any{
			inject.MetaStatus:  hashes,
			session.MetaSource: "kernel",
			session.MetaTrust:  "trusted",
		}
		if _, err := a.Core.Inject(session.KindSnapshot, inject.Render(sentSnaps), meta); err != nil {
			return false, err
		}
		injected = true
	}

	// L3 状态更新：只发变化的 key，合并成一条 <status-update>；超出总预算的低优先级条目丢弃，
	// 丢弃的 key 不更新哈希，下次再试（12.5）。
	changed := inject.Changes(stats, prev)
	// 撤销对全部片段算：prev 里也有快照的 key，只拿状态比会把仍在的快照当成"已清除"，
	// 于是在"发快照 / 发已清除"之间来回（2026-11-04 审查 P0-10）。
	cleared := inject.Cleared(items, prev)
	if len(changed) == 0 && len(cleared) == 0 {
		return injected, nil
	}
	kept, dropped := inject.Budget(changed, inject.DefaultTotalBudget)
	sent := make([]inject.Item, 0, len(kept)+len(cleared))
	sent = append(sent, kept...)
	sent = append(sent, cleared...)
	for k, v := range inject.Hashes(kept) { // 按截断前原文记哈希（P0-11）
		effective[k] = v
	}
	for _, it := range cleared {
		delete(effective, it.Key)
	}
	meta := map[string]any{
		inject.MetaStatus:  effective,
		session.MetaSource: "kernel",
		session.MetaTrust:  "trusted",
	}
	if len(dropped) > 0 {
		keys := make([]string, 0, len(dropped))
		for _, d := range dropped {
			keys = append(keys, d.Key)
		}
		meta[inject.MetaDropped] = keys
	}
	if _, err := a.Core.Inject(session.KindStatus, inject.Render(sent), meta); err != nil {
		return false, err
	}
	return true, nil
}
