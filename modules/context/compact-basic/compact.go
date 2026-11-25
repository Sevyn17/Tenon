// Package compactbasic：压缩 basic（tasks 5.7，docs/90 - 归档/20 - 插件/02 - 上下文压缩.md）。
//
// 机制：每次模型调用前估算请求 token，超过阈值就把较早的日志用侧调用总结成摘要，
// 本步请求按"假设压缩"视图发出（BuildWithCompaction），摘要以 Core.Compact 暂存、步边界落条目。
// 压缩发生在 modelhub 全部层之外（最外层），其后的层看到的已是压缩后的请求，R2 结构校验不受影响。
package compactbasic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Sevyn17/tenon/runtime/modelhub"
	"github.com/Sevyn17/tenon/runtime/reqlog"
	"github.com/Sevyn17/tenon/runtime/session"
)

// 默认值（DSH dsh-compaction-basic / pi-mono 对照见 kernel/docs/90 - 归档/20 - 插件/02 - 上下文压缩.md）。
const (
	DefaultHeadroom  = 16384 // 阈值里给工具结果与下一步留的余量
	DefaultKeepRatio = 0.16  // 保留最近 (W−O)×16% 的 token
)

var (
	// ErrDenied before_compact 拒绝。
	ErrDenied = errors.New("compact: before_compact 拒绝")
	// ErrNoShrink 摘要不比被替换内容短。
	ErrNoShrink = errors.New("compact: 摘要没有变短")
	// ErrEmpty 摘要为空。
	ErrEmpty = errors.New("compact: 摘要为空")
)

// Summarizer 用一次侧调用生成摘要文本。
type Summarizer func(ctx context.Context, req session.Request) (string, error)

// BeforeCompact before_compact 点位（乙类，由本插件开位）：返回错误即拒绝本次压缩（L3 记忆可在此先抽取）。
type BeforeCompact func(ctx context.Context, sessionID string, upto int) error

// Compactor 压缩器。
type Compactor struct {
	Auto      bool // false：只响应 /compact 与上下文超限
	Window    int  // 计划内最小上下文窗口（plugin-manager §5.2）；0 = 未知，不自动压缩
	MaxOutput int
	Headroom  int
	KeepRatio float64
	Model     string // 摘要所用模型（记入 meta）
	Summarize Summarizer
	Before    []BeforeCompact
	// befores 可空：运行时读 before_compact 点位上的登记（组件 Setup 时接好，见 plugin.go）。
	befores func() []BeforeCompact
	Redact  func(string) string // 可空；摘要落库前脱敏
}

// Threshold floor(min(W×0.8, W−O−B))；Window 未知返回 0。
func (c *Compactor) Threshold() int {
	if c.Window <= 0 {
		return 0
	}
	a := c.Window * 8 / 10
	b := c.Window - c.MaxOutput - c.headroom()
	if b < a {
		return b
	}
	return a
}

func (c *Compactor) headroom() int {
	if c.Headroom > 0 {
		return c.Headroom
	}
	return DefaultHeadroom
}

func (c *Compactor) keep() int {
	r := c.KeepRatio
	if r <= 0 {
		r = DefaultKeepRatio
	}
	return int(float64(c.Window-c.MaxOutput) * r)
}

// Estimate 请求 token 粗估（与 /stats 组成统计同一口径）。
func Estimate(req session.Request) int {
	return reqlog.Compose(req, len(req.Messages), nil, false).Total
}

// Plan 实现 agent.Compactor：需要压缩时返回视图与条目 meta；不需要返回 nil。
// force：上下文超限后的强制压缩（忽略阈值与 Auto）。
func (c *Compactor) Plan(ctx context.Context, sessionID string, core *session.Core, force bool) (*reqlog.CompactionView, map[string]any, error) {
	if !force {
		if !c.Auto || c.Threshold() == 0 {
			return nil, nil, nil
		}
		req, err := session.BuildRequest(core.Context())
		if err != nil {
			return nil, nil, err
		}
		if Estimate(req) < c.Threshold() {
			return nil, nil, nil
		}
	}
	upto := CutPoint(core, c.keep())
	if upto < 0 {
		return nil, nil, nil
	}
	return c.At(ctx, sessionID, core, upto)
}

// CutPoint 选切点：最大的合法 upto，使保留部分（abs ≥ upto）不少于 keep token。
// 下界是最近一个压缩条目之后——旧摘要必须在被总结的范围内，才能并入新摘要。无可用切点返回 -1。
func CutPoint(core *session.Core, keep int) int {
	cx := core.Context()
	lo := lowBound(cx, core.Snapshot().Header.CompactionBase)
	end := cx.Base + len(cx.Items)
	// 合法切点一次算出（审查 P1-40：原先对每个 u 调 CanCompact，每次从窗口开头扫，O(n²)）。
	ok := map[int]bool{}
	for _, u := range core.CompactPoints() {
		ok[u] = true
	}
	kept := 0
	for u := end; u > lo; u-- {
		if u < end {
			kept += entryTokens(cx.Items[u-cx.Base])
		}
		if kept >= keep && ok[u] {
			return u
		}
	}
	return -1
}

func lowBound(cx session.Context, base int) int {
	lo := base
	if cx.Base > lo {
		lo = cx.Base
	}
	for i := len(cx.Items) - 1; i >= 0; i-- {
		if cx.Items[i].Item.Kind == session.KindCompaction {
			if abs := cx.Base + i + 1; abs > lo {
				lo = abs
			}
			break
		}
	}
	return lo
}

func entryTokens(e *session.Entry) int {
	b, _ := json.Marshal(e.Item)
	return (len(b) + 3) / 4
}

// Instruction 摘要指令（索引式，Mortise docs/76 §16：原文留在库里可按需读回，摘要只做索引，不求保留全部细节）。
const Instruction = `请把以上对话压缩成一份索引式交接摘要，供接手者继续工作。原文仍保存在会话里，接手者需要细节时可以按回合号读回，所以不必抄录细节。不要调用工具，只输出摘要。
按以下小节组织，没有内容的小节写"（无）"：
## 目标与用户意图
## 进展索引（按时间分段，每段一句话，标出相关回合号，如"第 3–5 回合：……"）
## 关键决定（决定、原因、所在回合号）
## 未完成的事项
## 当前工作与下一步
## 必须记住的精确信息（路径、数值、约束；只列以后一定会用到的）
如果对话开头已有 <compacted-summary>，把其中仍然有效的内容合并进来，保留原有的回合号，不要原样照抄。`

// recallNote 摘要末尾由代码拼接的一句（固定中文，与 Instruction 同语言；范围由代码算，不交给模型抄写）。
func recallNote(first, last uint32) string {
	if first == 0 || last == 0 {
		return ""
	}
	if first == last {
		return fmt.Sprintf("第 %d 回合已压缩，原文可用 session_turns / session_read 查。", first)
	}
	return fmt.Sprintf("第 %d–%d 回合已压缩，原文可用 session_turns / session_read 查。", first, last)
}

// turnRange 被本次压缩覆盖的回合范围 [first, last]（docs/76 §16 C3c）：
// first 优先取窗口内上一个压缩条目 meta 的 turn_range 起点（多次压缩从第一个被压缩回合算起），
// 否则取窗口内 [Base, upto) 第一个非压缩条目的回合；last 取 [Base, upto) 非压缩条目的最大回合。
// 压缩条目自身的 Turn 是压缩发生时的回合，不计入。
func turnRange(cx session.Context, upto int) (first, last uint32, fromIdx int) {
	fromIdx = -1
	n := upto - cx.Base
	if n > len(cx.Items) {
		n = len(cx.Items)
	}
	for i := 0; i < n; i++ {
		e := cx.Items[i]
		if e.Item.Kind == session.KindCompaction {
			if f, ok := metaTurnStart(e.Meta); ok && (first == 0 || f < first) {
				first = f
			}
			continue
		}
		if fromIdx < 0 {
			fromIdx = cx.Base + i
		}
		if first == 0 || e.Turn < first {
			first = e.Turn
		}
		if e.Turn > last {
			last = e.Turn
		}
	}
	if fromIdx < 0 {
		fromIdx = cx.Base
	}
	return first, last, fromIdx
}

func metaTurnStart(m map[string]any) (uint32, bool) {
	r, ok := m["turn_range"]
	if !ok {
		return 0, false
	}
	var arr []float64
	switch v := r.(type) {
	case []any:
		for _, x := range v {
			if f, ok := x.(float64); ok {
				arr = append(arr, f)
			} else if i, ok := x.(int); ok {
				arr = append(arr, float64(i))
			} else if u, ok := x.(uint32); ok {
				arr = append(arr, float64(u))
			}
		}
	case []uint32:
		for _, x := range v {
			arr = append(arr, float64(x))
		}
	case []int:
		for _, x := range v {
			arr = append(arr, float64(x))
		}
	}
	if len(arr) == 0 || arr[0] <= 0 {
		return 0, false
	}
	return uint32(arr[0]), true
}

// At 在给定切点总结 [起点, upto)。before_compact 先于侧调用（被拒绝不花钱）。
func (c *Compactor) At(ctx context.Context, sessionID string, core *session.Core, upto int) (*reqlog.CompactionView, map[string]any, error) {
	if !core.CanCompact(upto) {
		return nil, nil, fmt.Errorf("compact: upto=%d 不是合法边界", upto)
	}
	for _, b := range c.allBefore() {
		if err := b(ctx, sessionID, upto); err != nil {
			return nil, nil, fmt.Errorf("%w: %v", ErrDenied, err)
		}
	}
	cx := core.Context()
	if upto < cx.Base {
		return nil, nil, fmt.Errorf("compact: upto=%d 早于加载窗口 %d", upto, cx.Base)
	}
	sub := cx
	sub.Items = cx.Items[:upto-cx.Base]
	// 与正式请求同一外壳与消息前缀：侧调用命中前缀缓存。
	req, err := session.BuildRequest(sub)
	if err != nil {
		return nil, nil, err
	}
	if len(req.Messages) == 0 {
		return nil, nil, nil
	}
	dropped := 0
	for _, m := range req.Messages {
		b, _ := json.Marshal(m)
		dropped += (len(b) + 3) / 4
	}
	req.Messages = append(req.Messages, session.Message{Role: "user", Content: session.Content{session.NewTextPart(Instruction)}})
	if c.Summarize == nil {
		return nil, nil, errors.New("compact: 未配置摘要模型")
	}
	text, err := c.Summarize(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	text = strings.TrimSpace(text)
	if c.Redact != nil { // 摘要会落库：过落库扫描（model-hub §side_call）
		text = c.Redact(text)
	}
	if text == "" {
		return nil, nil, ErrEmpty
	}
	first, last, fromIdx := turnRange(cx, upto)
	if note := recallNote(first, last); note != "" {
		text += "\n\n" + note // 代码拼接，计入下面的变短检查
	}
	sum := "<compacted-summary>\n" + text + "\n</compacted-summary>"
	if (len(sum)+3)/4 >= dropped {
		return nil, nil, ErrNoShrink
	}
	meta := map[string]any{session.MetaSource: "compact-basic", "summary_model": c.Model,
		"tokens_before": dropped, "tokens_after": (len(sum) + 3) / 4, "from_idx": fromIdx}
	if first > 0 {
		meta["turn_range"] = []uint32{first, last}
	}
	return &reqlog.CompactionView{Summary: session.Content{session.NewTextPart(sum)}, Upto: upto}, meta, nil
}

// HubSummarizer 经 modelhub 侧调用（不过层）生成摘要；只取文本，忽略工具调用。
func HubSummarizer(h *modelhub.Hub, alias string) Summarizer {
	return func(ctx context.Context, req session.Request) (string, error) {
		done, fail := h.SideCall(ctx, alias, req)
		if fail != nil {
			return "", fail
		}
		var sb strings.Builder
		for _, p := range done.Reply.Content {
			if p.Kind == "text" {
				sb.WriteString(p.Text)
			}
		}
		return sb.String(), nil
	}
}

// allBefore 显式设置的 Before 加上点位上登记的。
func (c *Compactor) allBefore() []BeforeCompact {
	if c.befores == nil {
		return c.Before
	}
	return append(append([]BeforeCompact(nil), c.Before...), c.befores()...)
}
