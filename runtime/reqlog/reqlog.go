// Package reqlog：结构化请求记录（review-1 R1、R2、G1）。
//
// 一次模型请求 = 外壳（model / system / tools / params）+ 前缀（日志区间，可带压缩视图）+ 后缀（Transform 追加的消息）。
// 存储只记这三部分的引用与 final_hash，存储量随步数线性增长；恢复时按三部分重组并用 final_hash 校验。
//
// 本包是宿主侧纯函数：不做 IO，不进内核。
package reqlog

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/Sevyn17/tenon/runtime/session"
)

// ErrShape：最终请求违反 Transform 结构约束（前缀被改、工具被放宽），按 Guard 失败处理（R2）。
var ErrShape = errors.New("reqlog: 最终请求结构不合法")

// ErrMismatch：重组结果与 final_hash 不符，调用方应换新 request_id（S7）。
var ErrMismatch = errors.New("reqlog: 重组请求哈希不符")

// CompactionView 本次请求所用的"假设压缩"视图（BuildWithCompaction 的参数）。
type CompactionView struct {
	Summary session.Content `json:"summary"`
	Upto    int             `json:"upto"`
}

// Shell 请求外壳：最终请求里除消息外的全部内容。
type Shell struct {
	Model  string                `json:"model"`
	System []session.SystemBlock `json:"system"`
	Tools  []session.ToolSpec    `json:"tools"`
	Params session.Params        `json:"params"`
}

// Record 结构化请求记录（与 store 的 session_requests 行一一对应）。
type Record struct {
	RequestID   string
	Shell       Shell
	PrefixBase  int
	PrefixEnd   int
	View        *CompactionView
	FinalHash   string
	Composition Composition
}

// Build 从内核上下文、基础请求与 Transform 之后的最终请求生成记录，并做 R2 结构校验：
// ① 最终请求的消息以基础请求的消息为前缀（逐字节相等），之后只能追加；
// ② 最终请求的 tools 是基础请求 tools 的子集（只能收窄）。
// sources[i] 给后缀第 i 条消息标来源，可为 nil。
func Build(requestID string, ctx session.Context, view *CompactionView, base, final session.Request) (*Record, error) {
	if len(final.Messages) < len(base.Messages) {
		return nil, fmt.Errorf("%w: 消息被删减（%d < %d）", ErrShape, len(final.Messages), len(base.Messages))
	}
	for i := range base.Messages {
		if !sameJSON(base.Messages[i], final.Messages[i]) {
			return nil, fmt.Errorf("%w: 前缀第 %d 条消息被改写", ErrShape, i)
		}
	}
	allowed := map[string]string{}
	for _, t := range base.Tools {
		b, _ := json.Marshal(t)
		allowed[t.Name] = string(b)
	}
	for _, t := range final.Tools {
		b, _ := json.Marshal(t)
		if w, ok := allowed[t.Name]; !ok || w != string(b) {
			return nil, fmt.Errorf("%w: 工具 %q 不在基础请求中或定义被改", ErrShape, t.Name)
		}
	}
	h, err := session.RequestHash(final)
	if err != nil {
		return nil, err
	}
	r := &Record{
		RequestID:  requestID,
		Shell:      Shell{Model: final.Model, System: final.System, Tools: final.Tools, Params: final.Params},
		PrefixBase: ctx.Base,
		PrefixEnd:  ctx.Base + len(ctx.Items),
		View:       view,
		FinalHash:  h,
	}
	// 注入已全部进内核历史（框架设计 12.4 C4），最终请求不再有「后缀」这一段；
	// 前缀之外的额外消息属于非法改写，由上面的前缀校验兜底（只允许追加，本版没有追加方）。
	r.Composition = Compose(final, len(base.Messages), nil, view != nil || hasCompaction(ctx))
	return r, nil
}

// Rebuild 按记录重组最终请求。ctx 必须覆盖 [PrefixBase, PrefixEnd)；哈希不符返回 ErrMismatch。
func Rebuild(r *Record, ctx session.Context) (session.Request, error) {
	if r.PrefixBase < ctx.Base || r.PrefixEnd > ctx.Base+len(ctx.Items) || r.PrefixEnd < r.PrefixBase {
		return session.Request{}, fmt.Errorf("%w: 前缀区间 [%d,%d) 不在加载窗口内", ErrMismatch, r.PrefixBase, r.PrefixEnd)
	}
	sub := ctx
	sub.Items = ctx.Items[r.PrefixBase-ctx.Base : r.PrefixEnd-ctx.Base]
	sub.Base = r.PrefixBase
	var base session.Request
	var err error
	if r.View != nil {
		base, err = session.BuildWithCompaction(sub, r.View.Summary, r.View.Upto)
	} else {
		base, err = session.BuildRequest(sub)
	}
	if err != nil {
		return session.Request{}, err
	}
	final := session.Request{Model: r.Shell.Model, System: r.Shell.System, Tools: r.Shell.Tools, Params: r.Shell.Params,
		Messages: base.Messages}
	h, err := session.RequestHash(final)
	if err != nil {
		return session.Request{}, err
	}
	if h != r.FinalHash {
		return session.Request{}, ErrMismatch
	}
	return final, nil
}

// —— 上下文组成（G1）——//

// Composition 各类上下文的 token 估算；Estimated 标明是估算值（拿到厂商用量后由上层校准）。
type Composition struct {
	Estimated bool           `json:"estimated"`
	Total     int            `json:"total"`
	Parts     map[string]int `json:"parts"`
}

// Compose 按类别估算 token：system:<来源>、tools、user、assistant、tool_result、compaction_summary、inject:<来源>。
// suffixFrom 是后缀起点（基础请求的消息数）；summaryFirst 表示首条消息是压缩摘要。
func Compose(req session.Request, suffixFrom int, sources []string, summaryFirst bool) Composition {
	p := map[string]int{}
	add := func(k string, n int) {
		if n > 0 {
			p[k] += n
		}
	}
	for _, s := range req.System {
		src := s.Source
		if src == "" {
			src = "agent"
		}
		add("system:"+src, est(len(s.Text)))
	}
	if len(req.Tools) > 0 {
		b, _ := json.Marshal(req.Tools)
		add("tools", est(len(b)))
	}
	for i, m := range req.Messages {
		n := est(msgLen(m))
		switch {
		case i >= suffixFrom:
			src := "transform"
			if j := i - suffixFrom; j < len(sources) && sources[j] != "" {
				src = sources[j]
			}
			add("inject:"+src, n)
		case i == 0 && m.Role == "user" && summaryFirst:
			add("compaction_summary", n)
		case m.Role == "tool":
			add("tool_result", n)
		default:
			add(m.Role, n)
		}
	}
	total := 0
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		total += p[k]
	}
	return Composition{Estimated: true, Total: total, Parts: p}
}

// hasCompaction：加载窗口内是否有压缩条目（有则请求首条消息是摘要）。
func hasCompaction(ctx session.Context) bool {
	for _, e := range ctx.Items {
		if e.Item.Kind == session.KindCompaction && len(e.Item.Compaction.Summary) > 0 {
			return true
		}
	}
	return false
}

// est：粗估 token（约 4 字节 1 token；中文按 UTF-8 3 字节，误差由厂商用量校准）。
func est(bytes int) int { return (bytes + 3) / 4 }

func msgLen(m session.Message) int {
	b, _ := json.Marshal(m)
	return len(b)
}

func sameJSON(a, b any) bool {
	x, e1 := json.Marshal(a)
	y, e2 := json.Marshal(b)
	return e1 == nil && e2 == nil && string(x) == string(y)
}
