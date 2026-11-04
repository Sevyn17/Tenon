// Package inject：上下文注入（框架设计 12，T2b）。
//
// 组件不直接写上下文，只提交片段（Contribution）：内核在每次调模型前收集，
// 按 key 只发变化的部分，渲染成带来源与信任标签的条目追加到历史末尾。
//
// 本包是纯函数：不做 IO、不感知存储与协议；由 agent 在模型调用前调用。
package inject

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	"ybase-kernel/core/callctx"
	"ybase-kernel/core/session"
)

// Kind 片段种类（12.5）。
type Kind string

const (
	// KindSnapshot 会话快照（L2）：AGENTS.md、技能目录、环境；会话开头与压缩后注入一次。
	KindSnapshot Kind = "snapshot"
	// KindStatus 状态更新（L3）：每次调模型前收集，只发变化的 key，渲染在本次请求末尾。
	KindStatus Kind = "status"
)

// 信任标签（12.5）：由内核填，组件不能自己写。
const (
	TrustInternal = "internal"
	TrustExternal = "external"
)

// 预算缺省值（12.5；总上限可配，本版先用包级变量）。
var (
	// DefaultItemBudget 单条片段的 token 上限，超出截断。
	DefaultItemBudget = 400
	// DefaultTotalBudget 单次注入的 token 总上限，超出按 Priority 丢低的。
	DefaultTotalBudget = 2000
)

// Item 一条待注入片段（12.5 Contribution）。
type Item struct {
	Key      string // 稳定键，如 agents_md:/repo/app、todo、time
	Kind     Kind   // 缺省 Status
	Body     string
	Priority int    // 超总上限时先丢小的；缺省 0
	Budget   int    // 单条 token 上限；<=0 用 DefaultItemBudget
	Source   string // 内核填：组件名
	Trust    string // 内核填：internal / external
	Cleared  bool   // 渲染成「已清除」（key 被撤销）；由 Cleared() 生成
	// orig 截断前的原文；哈希一律按它算。按截断后的文本记哈希、按原文比较，超预算的片段内容没变也会
	// 每次重发（2026-11-04 审查 P0-11）。空 = Body 未截断。
	orig string
}

// hashBody 片段用于"是否变化"判断的哈希：按截断前的原文。
func (it Item) hashBody() string {
	if it.orig != "" {
		return Hash(it.orig)
	}
	return Hash(it.Body)
}

// cut 截断 Body，原文记进 orig。
func (it Item) cut(budget int) Item {
	if t := truncate(it.Body, budget); t != it.Body {
		if it.orig == "" {
			it.orig = it.Body
		}
		it.Body = t
	}
	return it
}

// Func 状态提交函数：每次调模型前被调用，返回当前片段。
type Func func(*callctx.Ctx) []Item

// Reg 一份状态提交登记（组件名 + 键 + 函数；键可 * 结尾表示动态前缀）。
type Reg struct {
	Key   string
	Owner string
	Fn    Func
}

// MetaStatus / MetaDropped 注入条目 meta 键：已发哈希表与本次被丢弃的键。
const (
	MetaStatus  = "status"
	MetaDropped = "dropped"
)

// Estimate 粗估 token（约 4 字节 1 token；与 reqlog 的估算一致）。
func Estimate(s string) int { return (len(s) + 3) / 4 }

// Hash 片段内容哈希（sha256 十六进制）。
func Hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// Collect 依次调用登记函数并补全来源/信任/缺省种类。函数为 nil 跳过。
func Collect(ctx *callctx.Ctx, regs []Reg) []Item {
	var out []Item
	for _, r := range regs {
		if r.Fn == nil {
			continue
		}
		for _, it := range r.Fn(ctx) {
			if strings.TrimSpace(it.Body) == "" {
				continue
			}
			if it.Kind == "" {
				it.Kind = KindStatus
			}
			if it.Key == "" {
				it.Key = strings.TrimSuffix(r.Key, "*")
			}
			if it.Source == "" {
				it.Source = r.Owner
			}
			if it.Trust == "" {
				it.Trust = TrustInternal
			}
			out = append(out, it)
		}
	}
	return out
}

// Hashes 计算片段哈希表。
func Hashes(items []Item) map[string]string {
	out := make(map[string]string, len(items))
	for _, it := range items {
		out[it.Key] = it.hashBody()
	}
	return out
}

// HashesFrom 从已写入历史的注入条目推导已发哈希表；同一 key 以后者为准。
// 兼容 JSON round trip 后的 map[string]any。
//
// 内核写的 status 条目记的是「发完这条之后的全部有效哈希」（含先前的、去掉已清除的），所以以最后一条
// 内核 status 条目为准，再叠上它之后的快照条目（快照只记自己发的 key）。不能把所有条目的表都合起来：
// 已清除的 key 会从更早的条目里复活，之后每次调模型都多发一条「清除」（D 组）。
// notice 信封转成的 status 条目不带哈希表，不参与。
func HashesFrom(entries []*session.Entry) map[string]string {
	start := 0
	for i := len(entries) - 1; i >= 0; i-- {
		if e := entries[i]; e != nil && e.Item.Kind == session.KindStatus && e.Meta[MetaStatus] != nil {
			start = i
			break
		}
	}
	out := map[string]string{}
	for _, e := range entries[start:] {
		if e == nil || (e.Item.Kind != session.KindStatus && e.Item.Kind != session.KindSnapshot) {
			continue
		}
		switch v := e.Meta[MetaStatus].(type) {
		case map[string]string:
			for k, h := range v {
				out[k] = h
			}
		case map[string]any:
			for k, h := range v {
				if s, ok := h.(string); ok {
					out[k] = s
				}
			}
		}
	}
	return out
}

// Changes 按已发哈希表挑出变化的片段：同 key 后者取代前者，顺序按 key 排序保证重放一致。
func Changes(items []Item, prev map[string]string) []Item {
	byKey := map[string]Item{}
	order := make([]string, 0, len(items))
	for _, it := range items {
		if _, ok := byKey[it.Key]; !ok {
			order = append(order, it.Key)
		}
		byKey[it.Key] = it
	}
	kept := make([]Item, 0, len(order))
	for _, k := range order {
		it := byKey[k]
		if h, ok := prev[k]; ok && h == it.hashBody() {
			continue
		}
		kept = append(kept, it)
	}
	sort.SliceStable(kept, func(i, j int) bool {
		if kept[i].Priority != kept[j].Priority {
			return kept[i].Priority > kept[j].Priority
		}
		return kept[i].Key < kept[j].Key
	})
	return kept
}

// Cleared 返回已发过、本次不再出现的 key（撤销：写一条「已清除」）。
func Cleared(items []Item, prev map[string]string) []Item {
	now := map[string]bool{}
	for _, it := range items {
		now[it.Key] = true
	}
	var keys []string
	for k := range prev {
		if !now[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	out := make([]Item, 0, len(keys))
	for _, k := range keys {
		out = append(out, Item{Key: k, Kind: KindStatus, Cleared: true})
	}
	return out
}

// Fit 只做单条截断（快照用：不占状态更新的总预算）。
func Fit(items []Item) []Item {
	out := make([]Item, len(items))
	for i, it := range items {
		out[i] = it.cut(budgetOf(it))
	}
	return out
}

// Dropped 一条因超出预算被丢弃的片段（12.5：丢弃的 key 不更新哈希，下次再试）。
type Dropped struct {
	Key    string
	Tokens int
}

// Budget 先按单条上限截断，再按总上限按 Priority 从低到高丢弃。返回保留项与被丢项。
func Budget(items []Item, total int) ([]Item, []Dropped) {
	if total <= 0 {
		total = DefaultTotalBudget
	}
	sorted := make([]Item, len(items))
	copy(sorted, items)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Priority != sorted[j].Priority {
			return sorted[i].Priority > sorted[j].Priority
		}
		return sorted[i].Key < sorted[j].Key
	})
	var (
		kept    []Item
		dropped []Dropped
		used    int
	)
	for _, it := range sorted {
		it = it.cut(budgetOf(it))
		n := Estimate(it.Body)
		if used+n > total {
			dropped = append(dropped, Dropped{Key: it.Key, Tokens: n})
			continue
		}
		used += n
		kept = append(kept, it)
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].Key < kept[j].Key })
	return kept, dropped
}

func budgetOf(it Item) int {
	if it.Budget > 0 {
		return it.Budget
	}
	return DefaultItemBudget
}

// truncate 按 token 预算截断（4 字节 1 token），截断处留标记。
func truncate(s string, budget int) string {
	if budget <= 0 || Estimate(s) <= budget {
		return s
	}
	maxBytes := budget * 4
	if maxBytes >= len(s) {
		return s
	}
	// 回退到 UTF-8 字符边界，避免截断多字节字符。
	cut := maxBytes
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "\n…（已截断）"
}

// Render 把片段渲染成注入文本：快照用 <context>，状态更新合并成一条 <status-update>。
// 片段内容里的同名标签会被转义，防止伪造。
func Render(items []Item) string {
	var b strings.Builder
	wrote := false
	for _, it := range items {
		if it.Kind != KindSnapshot {
			continue
		}
		if wrote {
			b.WriteString("\n")
		}
		b.WriteString("<context" + attrs(it) + ">\n")
		b.WriteString(escapeBody(it.Body))
		b.WriteString("\n</context>")
		wrote = true
	}
	hasStatus := false
	for _, it := range items {
		if it.Kind == KindStatus || it.Kind == "" {
			hasStatus = true
			break
		}
	}
	if !hasStatus {
		return b.String()
	}
	if wrote {
		b.WriteString("\n")
	}
	b.WriteString("<status-update>\n")
	for _, it := range items {
		if it.Kind != KindStatus && it.Kind != "" {
			continue
		}
		b.WriteString("<item" + attrs(it))
		if it.Cleared {
			b.WriteString(` cleared="true">已清除</item>` + "\n")
			continue
		}
		b.WriteString(">" + escapeBody(it.Body) + "</item>\n")
	}
	b.WriteString("</status-update>")
	return b.String()
}

var bodyEscaper = strings.NewReplacer(
	"<status-update", "&lt;status-update",
	"</status-update", "&lt;/status-update",
	"<context", "&lt;context",
	"</context", "&lt;/context",
	"<item", "&lt;item",
	"</item", "&lt;/item",
)

func escapeBody(s string) string { return bodyEscaper.Replace(s) }

// attrs 拼出非空属性（撤销条目没有来源/信任，不写空属性）。
func attrs(it Item) string {
	var b strings.Builder
	if it.Source != "" {
		b.WriteString(` source="` + escapeAttr(it.Source) + `"`)
	}
	if it.Key != "" {
		b.WriteString(` key="` + escapeAttr(it.Key) + `"`)
	}
	if it.Trust != "" {
		b.WriteString(` trust="` + escapeAttr(it.Trust) + `"`)
	}
	return b.String()
}

var attrEscaper = strings.NewReplacer(`&`, "&amp;", `"`, "&quot;", "<", "&lt;", ">", "&gt;")

func escapeAttr(s string) string { return attrEscaper.Replace(s) }
