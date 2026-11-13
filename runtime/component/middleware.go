package component

// 洋葱式中间件链（框架设计 04 4.3）。
//
// 同步扩展只有一种形式：中间件包在内核固定点位外面。每个点位带自己的请求 / 响应类型，
// 中间件可以调用 next 继续、也可以不调（短路 = 拦截）。
//
// 与「守卫」的关系：守卫在最内层、紧贴内核执行，中间件改写后的请求仍要过守卫（08）。
// 与「点位登记」（RegisterAt）的区别：那是「按会话构造时取贡献」，这是「每次调用时包一层」。

import (
	"context"
	"fmt"
	"sync"
)

// Middleware 一个中间件：拿到请求，可以调用 next 继续，也可以直接返回（短路）。
// next 的类型与中间件一致，类型不匹配编译不过。
type Middleware[Req, Resp any] func(ctx context.Context, req Req, next func(context.Context, Req) (Resp, error)) (Resp, error)

// MwPoint 一个中间件点位：点位名 + 请求 / 响应类型。零值不可用，用 NewMwPoint 构造。
type MwPoint[Req, Resp any] struct{ name string }

// NewMwPoint 构造中间件点位。name 用 Point* 常量。
func NewMwPoint[Req, Resp any](name string) MwPoint[Req, Resp] { return MwPoint[Req, Resp]{name: name} }

// Name 点位名（explain 用）。
func (p MwPoint[Req, Resp]) Name() string { return p.name }

// mwReg 一条已登记的中间件（按点位名存）。
//
// 注意：类型**不是**由编译器保证的。`Wrap` 的 `p` 与 `mw` 都是泛型参数，
// 但登记表按点位名（string）存，所以 `Wrap(cx, ToolCall, ..., 一个 Deliver 签名的中间件)`
// 能编译通过，只在运行时才发现 req 的类型对不上。见 Wrap 的注释。
type mwReg struct {
	plugin string
	order  int
	run    func(ctx context.Context, req any, next func(context.Context, any) (any, error)) (any, error)
}

// Wrap 向点位登记一个中间件（04 4.3）。同一点位按登记顺序包：先登记的在外层。
//
// 类型不匹配**不是**编译错误：`p` 只用来取点位名，`mw` 的类型是独立的泛型参数。
// 登记按名字存，求值时把 req / 响应断言成 Req / Resp；断言失败时 req 取零值、
// 响应取零值，中间件照常执行。写错类型编译器拦不住，请自己核对点位与签名是否配对。
func Wrap[Req, Resp any](cx *Registrar, p MwPoint[Req, Resp], mw Middleware[Req, Resp]) {
	if mw == nil {
		return
	}
	name := p.name
	reg := mwReg{plugin: cx.plugin, order: len(cx.asm.middleware[name]),
		run: func(ctx context.Context, req any, next func(context.Context, any) (any, error)) (any, error) {
			r, _ := req.(Req)
			return mw(ctx, r, func(c context.Context, rr Req) (Resp, error) {
				out, err := next(c, rr)
				resp, _ := out.(Resp)
				return resp, err
			})
		}}
	cx.asm.middleware[name] = append(cx.asm.middleware[name], reg)
	cx.asm.points = append(cx.asm.points, Registration{Plugin: cx.plugin, Point: "middleware:" + name, Name: cx.plugin})
}

// MiddlewareNames 某点位上已登记的中间件（按顺序），explain 用。
func (a *Assembly) MiddlewareNames(point string) []string {
	regs := a.middleware[point]
	out := make([]string, len(regs))
	for i, r := range regs {
		out[i] = r.plugin
	}
	return out
}

// Chain 把一个点位上的中间件与内核实现串成洋葱链：kernel 是最内层（紧贴内核）。
// 返回的函数按「先登记的在外层」执行。没有中间件时直接调 kernel。
func Chain[Req, Resp any](regs []mwReg, kernel func(context.Context, Req) (Resp, error)) func(context.Context, Req) (Resp, error) {
	if len(regs) == 0 {
		return kernel
	}
	var once sync.Once
	var chain func(context.Context, Req) (Resp, error)
	once.Do(func() {
		// 从最内层往外包：最后一个登记的最靠内。
		chain = kernel
		for i := len(regs) - 1; i >= 0; i-- {
			inner := chain
			r := regs[i]
			chain = func(ctx context.Context, req Req) (Resp, error) {
				out, err := r.run(ctx, req, func(c context.Context, rr any) (any, error) {
					// 这里断言必定成立：rr 由外层中间件按 Req 传入（上面 mw 的 next 签名
					// 就是 func(..., Req)），只有写错类型的中间件才可能破坏它——那种情况
					// 在 Wrap 里已经退化成零值 req，不会走到这里。
					return inner(c, rr.(Req))
				})
				resp, _ := out.(Resp)
				return resp, err
			}
		}
	})
	return chain
}

// ErrNoMiddlewarePoint 点位名没有登记过任何中间件。
var ErrNoMiddlewarePoint = fmt.Errorf("component: 点位上没有中间件")
