package toolhub

// 工具调用的两类扩展（04 4.3.1、4.3.2）：改写与准入。
//
// 原来是**一个** `Layer` 结构体 + 一个 `Guard bool` 同时编码两种语义，长出三个补丁：
//   · `Guard bool` 同时表示「有没有拒绝权」和「排在内层还是外层」
//   · `Timeout` / `FailOpen` 只对改写层有效，写在准入层上是死字段（编译通过、运行无效）
//   · `Call` 谁都能返回 `Confirm`，于是要运行时检查「只有 Guard 能返回 Confirm」
//
// 拆成两个类型后，这些补丁全部消失：
//   · `Transform` 只能改，`Gate` 只能判——编译期就分开了
//   · `Transform` 才有 `Timeout`/`FailOpen`，`Gate` 没有这两个字段
//   · `Gate` 返回 `Verdict`（Allow/Deny/Confirm），`Transform` 返回 `Output`（没有 Confirm 选项）
//
// 与 `Authorizer` 的分工：`Gate` 是**纯判定**（无副作用、可并行、不能问人）；
// `Authorizer` 是**有状态的决策者**（可挂起问人、按恢复重判、单点拍板）。

import (
	"context"
	"encoding/json"
	"time"
)

// Transform 改写层（04 4.3.2）：包住一次工具调用，可以改参数或改结果，**不能拒绝**。
//
// 执行位置：在准入（`Gate` / `Authorizer`）**外面**——先让改写层洗干净参数，
// 再交给准入判定（「改写绕过守卫」不成立）。
type Transform struct {
	Name string
	// Timeout 本层自身耗时上限（不含 next 的耗时）。0 = 不限。
	Timeout time.Duration
	// FailOpen 本层超时/panic 时是否放行：true = 用内层结果、跳过本层改动；false = 拒绝整个调用。
	// 只有改写层有这个概念——准入层永远 fail-closed。
	FailOpen bool
	// Call 本层的实现。next 是下一环（可能是另一个改写层，也可能是准入 + 执行器）。
	Call func(ctx context.Context, cx *Cx, args json.RawMessage, next Next) (Output, error)
}

// Gate 准入层（04 4.3.2）：判断这次调用准不准，**不改写、不执行**。
//
// 与 `Transform` 的区别：没有 `next`（纯谓词）、没有 `Timeout`/`FailOpen`
// （准入永远 fail-closed）、可以返回 `Confirm`（要求人工确认）。
//
// 执行位置：在所有改写层**里面**（拿最终参数），在 `Authorizer` 之前。
// 多条 `Gate` **并行**跑，任一 Deny 即拒（reduce），不调 next。
type Gate struct {
	Name string
	// Check 判定。返回 (结论, 拒绝原因)；原因进 guard.denied 与工具结果。
	// 必须是纯谓词：不改参数、不做外部副作用、不依赖执行顺序。
	Check func(ctx context.Context, cx *Cx, args json.RawMessage) (Verdict, string)
}

// Verdict 一条准入判定的结论（与 component 的 Verdict 语义一致）。
type Verdict int

const (
	// VerdictAbstain 不表态，交给其它判定与内核缺省。
	VerdictAbstain Verdict = iota
	// VerdictAllow 允许（不能放开联系名单、预算、跳数、租户边界等内核不变量）。
	VerdictAllow
	// VerdictDeny 拒绝。
	VerdictDeny
	// VerdictConfirm 需要人工确认（挂起，等批准后按同一参数重放）。
	VerdictConfirm
)

func (v Verdict) String() string {
	switch v {
	case VerdictAllow:
		return "allow"
	case VerdictDeny:
		return "deny"
	case VerdictConfirm:
		return "confirm"
	default:
		return "abstain"
	}
}

// AddTransform 追加一个改写层（由外到内，调用方按 plugin-manager §5.2 排好序）。
func (h *Hub) AddTransform(t Transform) { h.transforms = append(h.transforms, t) }

// PrependTransform 在最外层插一个改写层（内核守卫先于组件层执行，如信封 caps 范围）。
func (h *Hub) PrependTransform(t Transform) {
	h.transforms = append([]Transform{t}, h.transforms...)
}

// AddGate 追加一条准入判定（并行跑，顺序不影响结论）。
func (h *Hub) AddGate(g Gate) { h.gates = append(h.gates, g) }

// Transforms 当前改写层（按由外到内；explain 用）。
func (h *Hub) Transforms() []Transform { return append([]Transform(nil), h.transforms...) }

// Gates 当前准入判定（explain 用）。
func (h *Hub) Gates() []Gate { return append([]Gate(nil), h.gates...) }
