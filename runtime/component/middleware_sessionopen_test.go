package component

// SessionOpen 中间件点位（04 4.3）：拒绝创建、只在新建时跑、顺序。

import (
	"context"
	"testing"
)

// 顺序：先登记在外层。
func TestSessionOpenChainOrder(t *testing.T) {
	var order []string
	pa := regMw(t, "sa", func(cx *Registrar) error {
		Wrap(cx, SessionOpen, func(ctx context.Context, req SessionOpenReq, next func(context.Context, SessionOpenReq) (SessionOpenResp, error)) (SessionOpenResp, error) {
			order = append(order, "a-in")
			r, err := next(ctx, req)
			order = append(order, "a-out")
			return r, err
		})
		return nil
	})
	pb := regMw(t, "sb", func(cx *Registrar) error {
		Wrap(cx, SessionOpen, func(ctx context.Context, req SessionOpenReq, next func(context.Context, SessionOpenReq) (SessionOpenResp, error)) (SessionOpenResp, error) {
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
	kernel := func(ctx context.Context, req SessionOpenReq) (SessionOpenResp, error) {
		order = append(order, "kernel")
		return SessionOpenResp{}, nil
	}
	if _, err := asm.SessionOpenChain(kernel)(context.Background(), SessionOpenReq{SessionID: "s1"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"a-in", "b-in", "kernel", "b-out", "a-out"}
	for i := range want {
		if i >= len(order) || order[i] != want[i] {
			t.Fatalf("顺序 = %v，期望 %v", order, want)
		}
	}
}

// 拒绝创建：Denied 为真，内核不执行。
func TestSessionOpenChainDeny(t *testing.T) {
	p := regMw(t, "deny", func(cx *Registrar) error {
		Wrap(cx, SessionOpen, func(ctx context.Context, req SessionOpenReq, next func(context.Context, SessionOpenReq) (SessionOpenResp, error)) (SessionOpenResp, error) {
			return SessionOpenResp{Denied: true, Reason: "不允许建会话"}, nil
		})
		return nil
	})
	asm, err := Assemble(Spec{Plugins: entries(p)})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	kernel := func(ctx context.Context, req SessionOpenReq) (SessionOpenResp, error) {
		called = true
		return SessionOpenResp{}, nil
	}
	resp, err := asm.SessionOpenChain(kernel)(context.Background(), SessionOpenReq{SessionID: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Denied || resp.Reason != "不允许建会话" || called {
		t.Fatalf("应拒绝且内核不执行：resp=%+v called=%v", resp, called)
	}
}

// 中间件能改写 Meta。
func TestSessionOpenChainRewritesMeta(t *testing.T) {
	p := regMw(t, "meta", func(cx *Registrar) error {
		Wrap(cx, SessionOpen, func(ctx context.Context, req SessionOpenReq, next func(context.Context, SessionOpenReq) (SessionOpenResp, error)) (SessionOpenResp, error) {
			req.Meta = []byte(`{"tenant":"t1"}`)
			return next(ctx, req)
		})
		return nil
	})
	asm, err := Assemble(Spec{Plugins: entries(p)})
	if err != nil {
		t.Fatal(err)
	}
	var seen string
	kernel := func(ctx context.Context, req SessionOpenReq) (SessionOpenResp, error) {
		seen = string(req.Meta)
		return SessionOpenResp{}, nil
	}
	if _, err := asm.SessionOpenChain(kernel)(context.Background(), SessionOpenReq{SessionID: "s1"}); err != nil {
		t.Fatal(err)
	}
	if seen != `{"tenant":"t1"}` {
		t.Fatalf("内核应看到改写后的 Meta，实际 %q", seen)
	}
}

// 没有中间件时链就是内核。
func TestSessionOpenChainEmpty(t *testing.T) {
	asm, err := Assemble(Spec{})
	if err != nil {
		t.Fatal(err)
	}
	if asm.HasMiddleware(PointSessionOpenMw) {
		t.Fatal("没登记时不该有中间件")
	}
	called := false
	kernel := func(ctx context.Context, req SessionOpenReq) (SessionOpenResp, error) {
		called = true
		return SessionOpenResp{}, nil
	}
	if _, err := asm.SessionOpenChain(kernel)(context.Background(), SessionOpenReq{}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("没中间件时应直接调内核")
	}
}
