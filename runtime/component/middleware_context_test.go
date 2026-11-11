package component

// Context 中间件点位（04 4.3）：顺序、改写 Messages、**不能改系统提示词**。

import (
	"context"
	"testing"

	"github.com/Sevyn17/tenon/runtime/session"
)

// 顺序：先登记在外层。
func TestContextChainOrder(t *testing.T) {
	var order []string
	pa := regMw(t, "ca", func(cx *Registrar) error {
		Wrap(cx, Context, func(ctx context.Context, req ContextReq, next func(context.Context, ContextReq) (ContextResp, error)) (ContextResp, error) {
			order = append(order, "a-in")
			r, err := next(ctx, req)
			order = append(order, "a-out")
			return r, err
		})
		return nil
	})
	pb := regMw(t, "cb", func(cx *Registrar) error {
		Wrap(cx, Context, func(ctx context.Context, req ContextReq, next func(context.Context, ContextReq) (ContextResp, error)) (ContextResp, error) {
			order = append(order, "b-in")
			r, err := next(ctx, req)
			order = append(order, "b-out")
			return r, err
		})
		return nil
	})
	asm, err := Assemble(Spec{Plugins: entries(pa, pb)})
	if err != nil {
		t.Fatal(err)
	}
	kernel := func(ctx context.Context, req ContextReq) (ContextResp, error) {
		order = append(order, "kernel")
		return ContextResp{}, nil
	}
	if _, err := asm.ContextChain(kernel)(context.Background(), ContextReq{Request: &session.Request{}}); err != nil {
		t.Fatal(err)
	}
	want := []string{"a-in", "b-in", "kernel", "b-out", "a-out"}
	for i := range want {
		if i >= len(order) || order[i] != want[i] {
			t.Fatalf("顺序 = %v，期望 %v", order, want)
		}
	}
}

// 中间件能改写 Messages（重排 / 改写渲染 / 附加进历史）。
func TestContextChainRewritesMessages(t *testing.T) {
	p := regMw(t, "ctx", func(cx *Registrar) error {
		Wrap(cx, Context, func(ctx context.Context, req ContextReq, next func(context.Context, ContextReq) (ContextResp, error)) (ContextResp, error) {
			req.Request.Messages = append(req.Request.Messages, session.Message{Role: "user",
				Content: session.Content{session.NewTextPart("附加的")}})
			return next(ctx, req)
		})
		return nil
	})
	asm, err := Assemble(Spec{Plugins: entries(p)})
	if err != nil {
		t.Fatal(err)
	}
	var got int
	kernel := func(ctx context.Context, req ContextReq) (ContextResp, error) {
		got = len(req.Request.Messages)
		return ContextResp{}, nil
	}
	req := &session.Request{Messages: []session.Message{{Role: "user"}}}
	if _, err := asm.ContextChain(kernel)(context.Background(), ContextReq{Request: req}); err != nil {
		t.Fatal(err)
	}
	if got != 2 {
		t.Fatalf("内核应看到 2 条历史，实际 %d", got)
	}
}

// 中间件能读 System（只读）但不能靠它生效——限制 1 由适配层保证（见 assemble 的 ctxMW）。
// 这里验证 System 字段能被读到。
func TestContextChainSeesSystem(t *testing.T) {
	var seen int
	p := regMw(t, "sys", func(cx *Registrar) error {
		Wrap(cx, Context, func(ctx context.Context, req ContextReq, next func(context.Context, ContextReq) (ContextResp, error)) (ContextResp, error) {
			seen = len(req.System)
			return next(ctx, req)
		})
		return nil
	})
	asm, err := Assemble(Spec{Plugins: entries(p)})
	if err != nil {
		t.Fatal(err)
	}
	kernel := func(ctx context.Context, req ContextReq) (ContextResp, error) { return ContextResp{}, nil }
	sys := []session.SystemBlock{{Text: "内核提示词"}}
	if _, err := asm.ContextChain(kernel)(context.Background(), ContextReq{Request: &session.Request{}, System: sys}); err != nil {
		t.Fatal(err)
	}
	if seen != 1 {
		t.Fatalf("中间件应能读到 1 个系统块，实际 %d", seen)
	}
}

// 没有中间件时链就是内核。
func TestContextChainEmpty(t *testing.T) {
	asm, err := Assemble(Spec{})
	if err != nil {
		t.Fatal(err)
	}
	if asm.HasMiddleware(PointContextMw) {
		t.Fatal("没登记时不该有中间件")
	}
	called := false
	kernel := func(ctx context.Context, req ContextReq) (ContextResp, error) {
		called = true
		return ContextResp{}, nil
	}
	if _, err := asm.ContextChain(kernel)(context.Background(), ContextReq{Request: &session.Request{}}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("没中间件时应直接调内核")
	}
}
