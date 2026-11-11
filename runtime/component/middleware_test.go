package component

// 洋葱式中间件链（04 4.3）：顺序、短路、改写、与守卫的关系。

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/runtime/mail"
)

// regMw 造一个登记了中间件的组件并返回它的名字（配 entries 用）。
func regMw(t *testing.T, name string, setup func(cx *Registrar) error) string {
	t.Helper()
	return reg(t, name, &fake{setup: setup})
}

// 顺序：先登记的在外层（先跑），后登记的在里层。
func TestDeliverChainOrder(t *testing.T) {
	var order []string
	pa := regMw(t, "a", func(cx *Registrar) error {
		Wrap(cx, Deliver, func(ctx context.Context, req DeliverReq, next func(context.Context, DeliverReq) (DeliverResp, error)) (DeliverResp, error) {
			order = append(order, "a-in")
			r, err := next(ctx, req)
			order = append(order, "a-out")
			return r, err
		})
		return nil
	})
	pb := regMw(t, "b", func(cx *Registrar) error {
		Wrap(cx, Deliver, func(ctx context.Context, req DeliverReq, next func(context.Context, DeliverReq) (DeliverResp, error)) (DeliverResp, error) {
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
	kernel := func(ctx context.Context, req DeliverReq) (DeliverResp, error) {
		order = append(order, "kernel")
		return DeliverResp{Delivered: true}, nil
	}
	chain := asm.DeliverChain(kernel)
	if _, err := chain(context.Background(), DeliverReq{Envelope: &mail.Envelope{}}); err != nil {
		t.Fatal(err)
	}
	want := []string{"a-in", "b-in", "kernel", "b-out", "a-out"}
	if len(order) != len(want) {
		t.Fatalf("顺序 = %v，期望 %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("顺序 = %v，期望 %v", order, want)
		}
	}
}

// 短路：不调 next 就拦下，内核不执行。
func TestDeliverChainShortCircuit(t *testing.T) {
	p := regMw(t, "blocker", func(cx *Registrar) error {
		Wrap(cx, Deliver, func(ctx context.Context, req DeliverReq, next func(context.Context, DeliverReq) (DeliverResp, error)) (DeliverResp, error) {
			return DeliverResp{Delivered: false}, nil // 不调 next
		})
		return nil
	})
	asm, err := Assemble(Spec{Plugins: entries(p)})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	kernel := func(ctx context.Context, req DeliverReq) (DeliverResp, error) {
		called = true
		return DeliverResp{Delivered: true}, nil
	}
	resp, err := asm.DeliverChain(kernel)(context.Background(), DeliverReq{Envelope: &mail.Envelope{}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Delivered || called {
		t.Fatalf("短路后内核不该执行：resp=%+v called=%v", resp, called)
	}
}

// 改写：中间件能改信封的目标地址，内核看到改写后的。
func TestDeliverChainRewrite(t *testing.T) {
	p := regMw(t, "reroute", func(cx *Registrar) error {
		Wrap(cx, Deliver, func(ctx context.Context, req DeliverReq, next func(context.Context, DeliverReq) (DeliverResp, error)) (DeliverResp, error) {
			req.Envelope.To = mail.Address{Kind: mail.AddrSession, ID: "s2"}
			req.SessionID = "s2"
			return next(ctx, req)
		})
		return nil
	})
	asm, err := Assemble(Spec{Plugins: entries(p)})
	if err != nil {
		t.Fatal(err)
	}
	var seen string
	kernel := func(ctx context.Context, req DeliverReq) (DeliverResp, error) {
		seen = req.Envelope.To.ID
		return DeliverResp{Delivered: true}, nil
	}
	env := &mail.Envelope{To: mail.Address{Kind: mail.AddrSession, ID: "s1"}}
	if _, err := asm.DeliverChain(kernel)(context.Background(), DeliverReq{Envelope: env}); err != nil {
		t.Fatal(err)
	}
	if seen != "s2" {
		t.Fatalf("内核应看到改写后的目标，实际 %q", seen)
	}
}

// 中间件返回错误按正常错误传回。
func TestDeliverChainError(t *testing.T) {
	boom := errors.New("中间件拒绝")
	p := regMw(t, "err", func(cx *Registrar) error {
		Wrap(cx, Deliver, func(ctx context.Context, req DeliverReq, next func(context.Context, DeliverReq) (DeliverResp, error)) (DeliverResp, error) {
			return DeliverResp{}, boom
		})
		return nil
	})
	asm, err := Assemble(Spec{Plugins: entries(p)})
	if err != nil {
		t.Fatal(err)
	}
	kernel := func(ctx context.Context, req DeliverReq) (DeliverResp, error) {
		return DeliverResp{Delivered: true}, nil
	}
	if _, err := asm.DeliverChain(kernel)(context.Background(), DeliverReq{Envelope: &mail.Envelope{}}); !errors.Is(err, boom) {
		t.Fatalf("错误应传回：%v", err)
	}
}

// 没有中间件时链就是内核本身。
func TestDeliverChainEmpty(t *testing.T) {
	asm, err := Assemble(Spec{})
	if err != nil {
		t.Fatal(err)
	}
	if asm.HasMiddleware(PointDeliverMw) {
		t.Fatal("没登记时不该有中间件")
	}
	called := false
	kernel := func(ctx context.Context, req DeliverReq) (DeliverResp, error) {
		called = true
		return DeliverResp{Delivered: true}, nil
	}
	if _, err := asm.DeliverChain(kernel)(context.Background(), DeliverReq{Envelope: &mail.Envelope{}}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("没中间件时应直接调内核")
	}
}

// explain 能列出点位上中间件的顺序。
func TestMiddlewareNames(t *testing.T) {
	p := regMw(t, "a", func(cx *Registrar) error {
		Wrap(cx, Deliver, func(ctx context.Context, req DeliverReq, next func(context.Context, DeliverReq) (DeliverResp, error)) (DeliverResp, error) {
			return next(ctx, req)
		})
		return nil
	})
	asm, err := Assemble(Spec{Plugins: entries(p)})
	if err != nil {
		t.Fatal(err)
	}
	names := asm.MiddlewareNames(PointDeliverMw)
	// reg() 会给组件名加测试前缀，这里只验有一条且含 "a"。
	if len(names) != 1 || !strings.Contains(names[0], "a") {
		t.Fatalf("中间件顺序 = %v", names)
	}
}
