package toolhub

// 可用集与显露集（tool-hub D1）。
//
//   - 可用集：会话创建时由智能体定义 ∩ 已登记工具展开，之后冻结；权限、explain、审计以它为准。
//   - 显露集：实际发给模型的工具定义（内核 Config.Tools），只增不减。
//
// 默认不延迟：只有全部工具定义的估算 token 超过预算时，才把标了 Deferred 的工具（动态来源，如 MCP）
// 移出显露集，只在目录段里给"名称 + 一句描述"。模型调用 tool_search 拿完整定义，
// 宿主在步边界经内核 ConfigPatch.AddTools 追加进显露集。内置核心工具永不延迟。

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"tenon/runtime/session"
)

// Toolset 一个会话的工具视图。并发安全（tool_search 可能并行调用）。
type Toolset struct {
	available []string // 冻结，按登记顺序
	exposed   map[string]bool
	deferred  []string // 未显露的可用工具
	catalog   string   // 系统提示里的目录段（无延迟时为空）

	mu      sync.Mutex
	pending []session.ToolSpec // tool_search 选中、待步边界追加的
}

// CatalogBudget 目录段字节上限（先保证全部名称，剩余预算再分描述——参考 Codex tools_budget）。
const CatalogBudget = 4 << 10

// SpecsFor 展开并冻结可用集。allow 为 nil 表示全部登记工具；budgetTokens 为延迟显露预算（0 = 不延迟）。
func (h *Hub) SpecsFor(allow []string, budgetTokens int) *Toolset {
	ok := map[string]bool{}
	for _, n := range allow {
		ok[n] = true
	}
	ts := &Toolset{exposed: map[string]bool{}}
	total := 0
	for _, n := range h.order {
		if allow != nil && !ok[n] && !builtinTool(n) {
			continue
		}
		ts.available = append(ts.available, n)
		total += specTokens(h.tools[n].Spec)
	}
	needDefer := budgetTokens > 0 && total > budgetTokens
	for _, n := range ts.available {
		if !h.visibleTool(n) {
			// 档位把该工具判为 deny：不进显露集也不进目录段，模型看不到它。
			// 仍留在可用集里，万一模型凭历史记录调用，由权限层按 deny 拒绝。
			continue
		}
		if needDefer && h.tools[n].Deferred {
			ts.deferred = append(ts.deferred, n)
			continue
		}
		ts.exposed[n] = true
	}
	if len(ts.deferred) > 0 {
		ts.catalog = h.catalog(ts.deferred)
	} else {
		delete(ts.exposed, "tool_search") // 没有延迟工具就不显露 tool_search
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

// Catalog 延迟工具的目录段（加进系统提示，Source = "toolhub:catalog"）；无延迟时为空。
func (ts *Toolset) Catalog() string { return ts.catalog }

// TakePending 取出 tool_search 选中的定义，宿主在步边界经 ConfigPatch.AddTools 追加；同时并入显露集。
func (ts *Toolset) TakePending() []session.ToolSpec {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	p := ts.pending
	ts.pending = nil
	for _, s := range p {
		ts.exposed[s.Name] = true
	}
	return p
}

func (ts *Toolset) isAvailable(n string) bool {
	for _, a := range ts.available {
		if a == n {
			return true
		}
	}
	return false
}

func (h *Hub) catalog(names []string) string {
	var sb strings.Builder
	sb.WriteString("以下工具可用但未加载，需要时先用 tool_search 按名称或关键词加载完整定义：\n")
	sb.WriteString(strings.Join(names, ", "))
	left := CatalogBudget - sb.Len()
	if left <= 0 {
		return sb.String()
	}
	per := left / len(names)
	for _, n := range names {
		d := firstLine(h.tools[n].Spec.Description)
		if len(d) > per {
			d = Preview(d, "head", per) + "…"
		}
		if d != "" {
			fmt.Fprintf(&sb, "\n- %s：%s", n, d)
		}
	}
	return sb.String()
}

// searchTool 内置 tool_search：在本会话可用集的延迟工具里按名称 / 关键词查找，选中项下一步可调用。
func (h *Hub) searchTool() *Tool {
	schema := `{"type":"object","properties":{"query":{"type":"string","description":"关键词，或用逗号分隔的工具名"}},"required":["query"]}`
	return &Tool{
		Spec:   session.ToolSpec{Name: "tool_search", Description: "加载未显露工具的完整定义；加载后下一步即可调用", Schema: json.RawMessage(schema)},
		Risk:   Read,
		NoAuth: true, // 只加载工具定义，不碰用户数据
		Call: func(_ context.Context, cx *Cx, raw json.RawMessage) (Output, error) {
			var a struct{ Query string }
			_ = json.Unmarshal(raw, &a)
			ts := cx.Toolset
			if ts == nil {
				return Failf("本会话没有延迟加载的工具"), nil
			}
			q := strings.ToLower(strings.TrimSpace(a.Query))
			names := map[string]bool{}
			for _, part := range strings.Split(q, ",") {
				names[strings.TrimSpace(part)] = true
			}
			ts.mu.Lock()
			defer ts.mu.Unlock()
			var hit []string
			for _, n := range ts.deferred {
				if ts.exposed[n] {
					continue
				}
				d := strings.ToLower(h.tools[n].Spec.Description)
				if names[strings.ToLower(n)] || (q != "" && (strings.Contains(strings.ToLower(n), q) || strings.Contains(d, q))) {
					hit = append(hit, n)
				}
			}
			if len(hit) == 0 {
				return Text("没有匹配的未加载工具。"), nil
			}
			sort.Strings(hit)
			queued := map[string]bool{}
			for _, p := range ts.pending {
				queued[p.Name] = true
			}
			for _, n := range hit {
				if !queued[n] {
					ts.pending = append(ts.pending, h.tools[n].Spec)
				}
			}
			return Text("已加载：" + strings.Join(hit, ", ") + "。下一步即可调用。"), nil
		},
	}
}

func builtinTool(n string) bool { return n == "read_artifact" || n == "tool_search" }

func specTokens(s session.ToolSpec) int {
	b, _ := json.Marshal(s)
	return (len(b) + 3) / 4
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
