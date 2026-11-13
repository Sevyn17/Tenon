package toolhub

// 可用集与显露集（tool-hub D1）。
//
//   - 可用集：会话创建时由智能体定义 ∩ 已登记工具展开，之后冻结；权限、explain、审计以它为准。
//   - 显露集：实际发给模型的工具定义（内核 Config.Tools）。当前等于可用集（减去档位判 deny 的）。
//
// 2026-11-13（L12）：原「延迟显露」整套机制删除——`Deferred` 标记、`tool_search` 工具、
// 目录段、`TakePending`、`ConfigPatch.AddTools`。删的理由是铁律一的三问：
//
//	① 有生产者吗？`needDefer` 依赖 budgetTokens，而生产唯一调用是 SpecsFor(allow)，
//	   预算恒 0 → needDefer 恒 false → deferred 恒空。延迟分支从未执行过。
//	② 有消费者吗？`Toolset.Catalog()` 与 `TakePending` 只有测试在调；`tool_search` 因为
//	   deferred 恒空而被 `delete(ts.exposed, "tool_search")` 摘掉，模型永远看不到它。
//	③ 在生产路径上吗？不在。`mcp.go` 里那行 `Deferred: true` 因此毫无效果。
//
// 设计依据也不在现行设计里：该机制只存在于归档的 `90 - 归档/10 - 框架模块/04 - 工具管理器`
// （§4.2，标注「待拍板 D1」），`docs/01 - 设计文档/` 全部 13 篇零命中。按它的触发条件
// （工具定义超过最小上下文窗口 10%）实测也够不着：内置 fs 工具 6 个合计 783 tokens，
// 最紧的 32K 窗口预算 3200，占 24.5%，远未触发。
//
// 真要按需显露时，照归档方案重新引入（含预算来源与 ConfigPatch 追加路径），
// 不要留一条「形状完整但内部为空」的脚手架。

import (
	"github.com/Sevyn17/tenon/runtime/session"
)

// Toolset 一个会话的工具视图。
type Toolset struct {
	available []string // 冻结，按登记顺序
	exposed   map[string]bool
}

// SpecsFor 展开并冻结可用集。allow 为 nil 表示全部登记工具。
func (h *Hub) SpecsFor(allow []string) *Toolset {
	ok := map[string]bool{}
	for _, n := range allow {
		ok[n] = true
	}
	ts := &Toolset{exposed: map[string]bool{}}
	for _, n := range h.order {
		if allow != nil && !ok[n] && !builtinTool(n) {
			continue
		}
		ts.available = append(ts.available, n)
	}
	for _, n := range ts.available {
		if !h.visibleTool(n) {
			// 档位把该工具判为 deny：不进显露集，模型看不到它。
			// 仍留在可用集里，万一模型凭历史记录调用，由权限层按 deny 拒绝。
			continue
		}
		ts.exposed[n] = true
	}
	return ts
}

// Exposed 显露集的工具定义（按登记顺序），写入内核 Config.Tools。
func (h *Hub) Exposed(ts *Toolset) []session.ToolSpec {
	var out []session.ToolSpec
	for _, n := range ts.available {
		if ts.exposed[n] {
			out = append(out, h.tools[n].Spec)
		}
	}
	return out
}

// Available 可用集（冻结）。
func (ts *Toolset) Available() []string { return append([]string(nil), ts.available...) }

func (ts *Toolset) isAvailable(n string) bool {
	for _, a := range ts.available {
		if a == n {
			return true
		}
	}
	return false
}

// builtinTool 内核自带的机制工具：不受档位影响、不要求登记权限项。
func builtinTool(n string) bool { return n == "read_artifact" }
