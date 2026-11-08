package inject

// 注入编排（2026-11-04 审查 P1-59）：原先写在 agent 里，快照与状态两类条目的哈希、撤销、预算交织在一起，
// P0-10 / P0-11 都出在这里。收成纯函数后可以不起会话直接测。

import "github.com/Sevyn17/tenon/runtime/session"

// Write 一条要写进上下文的注入条目（Plan 的输出，调用方交给 session.Core.Inject）。
type Write struct {
	Kind string // session.KindSnapshot / session.KindStatus
	Text string
	Meta map[string]any
}

// Plan 按本次收集的片段与历史里已发的哈希，算出要写的注入条目（12.5）。
// reset=true 用于压缩之后：忽略已发哈希，快照与状态按 key 重新注入一次。
// 返回 0–2 条：先快照（L2），后状态更新（L3）。
func Plan(items []Item, history []*session.Entry, reset bool) []Write {
	prev := map[string]string{}
	if !reset {
		prev = HashesFrom(history)
	}
	var snaps, stats []Item
	for _, it := range items {
		if it.Kind == KindSnapshot {
			snaps = append(snaps, it)
		} else {
			stats = append(stats, it)
		}
	}
	effective := make(map[string]string, len(prev))
	for k, v := range prev {
		effective[k] = v
	}
	var out []Write

	// L2 快照：会话开头与压缩后注入一次，不占状态更新的总预算。
	var sentSnaps []Item
	if reset {
		sentSnaps = Fit(snaps)
	} else {
		sentSnaps = Changes(snaps, prev)
	}
	if len(sentSnaps) > 0 {
		hashes := Hashes(sentSnaps)
		for k, v := range hashes {
			effective[k] = v
		}
		out = append(out, Write{Kind: session.KindSnapshot, Text: Render(sentSnaps), Meta: kernelMeta(hashes)})
	}

	// L3 状态更新：只发变化的 key，合并成一条 <status-update>；超出总预算的低优先级条目丢弃，
	// 丢弃的 key 不更新哈希，下次再试。
	changed := Changes(stats, prev)
	// 撤销对全部片段算：prev 里也有快照的 key，只拿状态比会把仍在的快照当成"已清除"，
	// 于是在"发快照 / 发已清除"之间来回（P0-10）。
	cleared := Cleared(items, prev)
	if len(changed) == 0 && len(cleared) == 0 {
		return out
	}
	kept, dropped := Budget(changed, DefaultTotalBudget)
	sent := make([]Item, 0, len(kept)+len(cleared))
	sent = append(sent, kept...)
	sent = append(sent, cleared...)
	for k, v := range Hashes(kept) { // 按截断前原文记哈希（P0-11）
		effective[k] = v
	}
	for _, it := range cleared {
		delete(effective, it.Key)
	}
	meta := kernelMeta(effective)
	if len(dropped) > 0 {
		keys := make([]string, 0, len(dropped))
		for _, d := range dropped {
			keys = append(keys, d.Key)
		}
		meta[MetaDropped] = keys
	}
	return append(out, Write{Kind: session.KindStatus, Text: Render(sent), Meta: meta})
}

// kernelMeta 内核写的注入条目 meta：已发哈希表 + 来源与信任。
func kernelMeta(hashes map[string]string) map[string]any {
	return map[string]any{
		MetaStatus:         hashes,
		session.MetaSource: "kernel",
		session.MetaTrust:  "trusted",
	}
}
