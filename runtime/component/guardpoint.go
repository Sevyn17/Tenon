package component

// 判定区原语（04 4.3.1）：纯谓词，任一拒绝即拒绝（reduce），不接受 next、不能改写。
//
// 与守卫（4.2）的关系：守卫是**内核不变量**（紧贴内核、绕不过去），判定区是**扩展点**
// （组件登记准入规则）。两者结论合并规则相同（任一 Deny 即拒），所以复用 Verdict。
//
// 与中间件（Wrap）的关系：判定区在改写区**之内**——中间件改写完的请求才交给判定，
// 「改写绕过守卫」不成立。

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// GuardFn 一条判定规则的实现。返回 (结论, 拒绝原因)；原因进 guard.denied 事件。
//
// 必须是**纯谓词**：不改写请求、不做外部副作用、不调用 next。要改写请用 Wrap（改写区），
// 要挂起问人请用 Authorizer（决策服务）。
type GuardFn[Req any] func(ctx context.Context, req Req) (Verdict, string)

// guardReg 一条已登记的判定（按点位名存，类型由登记时保证）。
type guardReg struct {
	name  string
	order int
	check func(ctx context.Context, req any) (Verdict, string)
}

// Named 只有点位名的窄接口（04 4.3.1）。
//
// WrapGuard 不能直接接 MwPoint[Req, Resp]：Go 泛型无法从 MwPoint[ToolCallReq, ToolCallResp]
// 推出 MwPoint[ToolCallReq, any]（不支持部分类型推断）。所以判定区只要求「能给出点位名」，
// Req 由 GuardFn 的类型单独定。
type Named interface{ Name() string }

// WrapGuard 向点位登记一条判定规则（04 4.3.1）。
//
// 判定区并行跑（纯谓词无依赖），任一 Deny 即拒。`name` 进 explain 与拒绝原因。
//
// 类型不匹配**不是**编译错误：点位参数是 `Named`（只给名字），Req 由 fn 的类型单独定，
// 所以 `WrapGuard(cx, ToolCall, ..., func(..., DeliverReq) ...)` 能编译通过。
// 登记时按点位名存，求值时把 req 断言成 Req；断言失败会拿到零值 req 并静默继续，
// 而判定结论照样生效。写错类型不会被编译器拦住，请自己核对点位与 Req 是否配对。
func WrapGuard[Req any](cx *Registrar, p Named, name string, fn GuardFn[Req]) {
	if fn == nil {
		return
	}
	if name == "" {
		name = cx.plugin
	}
	point := p.Name()
	reg := guardReg{name: name, order: len(cx.asm.guardsByPoint[point]),
		check: func(ctx context.Context, req any) (Verdict, string) {
			r, ok := req.(Req)
			if !ok {
				// 类型不符：判定的前提已经不成立，不能拿零值 req 猜结论（那会让一条写错的
				// 判定按零值放行或拒绝，比不判更危险）。Abstain 等于这条规则不参与。
				return Abstain, ""
			}
			return fn(ctx, r)
		}}
	cx.asm.guardsByPoint[point] = append(cx.asm.guardsByPoint[point], reg)
	cx.asm.points = append(cx.asm.points, Registration{Plugin: cx.plugin, Point: "guard:" + point, Name: name})
}

// GuardNames 某点位上已登记的判定规则名（按登记顺序），explain 用。
func (a *Assembly) GuardNames(point string) []string {
	regs := a.guardsByPoint[point]
	out := make([]string, len(regs))
	for i, r := range regs {
		out[i] = r.name
	}
	return out
}

// HasGuards 某点位上是否登记了判定规则。
func (a *Assembly) HasGuards(point string) bool { return len(a.guardsByPoint[point]) > 0 }

// EvalPoint 跑某点位上的全部判定并合并结论：任一 Deny 即拒（原因为空用规则名），
// 否则有 Allow 为 Allow，全 Abstain 为 Abstain。
//
// 并行跑：每条判定互不依赖。为让拒绝原因稳定（便于测试与 explain），
// 多条 Deny 时取**登记顺序最靠前**的那条原因。
func EvalPoint[Req any](a *Assembly, point string, ctx context.Context, req Req) (Verdict, string) {
	regs := a.guardsByPoint[point]
	if len(regs) == 0 {
		return Abstain, ""
	}
	type result struct {
		idx     int
		verdict Verdict
		reason  string
	}
	results := make([]result, len(regs))
	var wg sync.WaitGroup
	for i, r := range regs {
		wg.Add(1)
		go func(i int, r guardReg) {
			defer wg.Done()
			v, why := r.check(ctx, req)
			results[i] = result{idx: i, verdict: v, reason: why}
		}(i, r)
	}
	wg.Wait()
	// 合并：任一 Deny 即拒；取登记顺序最靠前的 Deny 原因（确定性）。
	verdict := Abstain
	reason := ""
	var denies []result
	hasAllow := false
	for _, res := range results {
		switch res.verdict {
		case Deny:
			denies = append(denies, res)
		case Allow:
			hasAllow = true
		}
	}
	if len(denies) > 0 {
		sort.Slice(denies, func(i, j int) bool { return denies[i].idx < denies[j].idx })
		reason = denies[0].reason
		if reason == "" {
			reason = regs[denies[0].idx].name
		}
		return Deny, reason
	}
	if hasAllow {
		verdict = Allow
	}
	return verdict, ""
}

// Authorizer 决策服务（04 4.3.1）：判定一次工具调用准不准。
//
// 与判定区的区别：Authorizer **可以挂起问人**（等用户点确认）、可以有状态、
// 可以返回错误（基础设施故障）。判定区是纯谓词，Authorizer 不是。
//
// 只给 ToolCall 点位用——只有工具调用需要挂起问人。其余点位的准入用 WrapGuard 谓词就够。
type Authorizer interface {
	// Decide 判定一次工具调用。Verdict 为 Deny 时 reason 进 guard.denied；
	// 返回 error 表示判定本身失败（按失败即收紧处理）。
	Decide(ctx context.Context, req ToolCallReq) (Verdict, string, error)
}

// SetAuthorizer 登记决策服务（恰好一个；重复登记报错）。
func (cx *Registrar) SetAuthorizer(a Authorizer) error {
	if a == nil {
		return nil
	}
	if cx.asm.authorizer != nil {
		return fmt.Errorf("component: Authorizer 已被 %s 登记，%s 不能再登记（只允许一个）",
			cx.asm.authorizerPlugin, cx.plugin)
	}
	cx.asm.authorizer = a
	cx.asm.authorizerPlugin = cx.plugin
	cx.asm.points = append(cx.asm.points, Registration{Plugin: cx.plugin, Point: "authorizer", Name: cx.plugin})
	return nil
}

// Authorizer 取已登记的决策服务（没有返回 nil）。
func (a *Assembly) Authorizer() Authorizer { return a.authorizer }
