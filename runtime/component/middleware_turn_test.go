package component

// Turn 中间件点位（04 4.3）：顺序、End 改写、Skip。

import (
	"context"
	"encoding/json"
	"testing"
)

// 顺序：先登记在外层；kernel 在中间。
func TestTurnChainOrder(t *testing.T) {
	var order []string
	pa := regMw(t, "ta", func(cx *Registrar) error {
		Wrap(cx, Turn, func(ctx context.Context, req TurnReq, next func(context.Context, TurnReq) (TurnResp, error)) (TurnResp, error) {
			order = append(order, "a-in")
			r, err := next(ctx, req)
			order = append(order, "a-out")
			return r, err
		})
		return nil
	})
	pb := regMw(t, "tb", func(cx *Registrar) error {
		Wrap(cx, Turn, func(ctx context.Context, req TurnReq, next func(context.Context, TurnReq) (TurnResp, error)) (TurnResp, error) {
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
	kernel := func(ctx context.Context, req TurnReq) (TurnResp, error) {
		order = append(order, "kernel")
		return TurnResp{}, nil
	}
	if _, err := asm.TurnChain(kernel)(context.Background(), TurnReq{SessionID: "s1"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"a-in", "b-in", "kernel", "b-out", "a-out"}
	for i := range want {
		if i >= len(order) || order[i] != want[i] {
			t.Fatalf("顺序 = %v，期望 %v", order, want)
		}
	}
}

// 中间件能改写 End（如把挂起改成结束）。
func TestTurnChainRewritesEnd(t *testing.T) {
	p := regMw(t, "end", func(cx *Registrar) error {
		Wrap(cx, Turn, func(ctx context.Context, req TurnReq, next func(context.Context, TurnReq) (TurnResp, error)) (TurnResp, error) {
			resp, err := next(ctx, req)
			if err != nil {
				return resp, err
			}
			resp.End = json.RawMessage(`{"Kind":1,"Message":"改写后"}`)
			return resp, nil
		})
		return nil
	})
	asm, err := Assemble(Spec{Plugins: entries(p)})
	if err != nil {
		t.Fatal(err)
	}
	kernel := func(ctx context.Context, req TurnReq) (TurnResp, error) {
		return TurnResp{End: json.RawMessage(`{"Kind":0}`)}, nil
	}
	resp, err := asm.TurnChain(kernel)(context.Background(), TurnReq{})
	if err != nil {
		t.Fatal(err)
	}
	if string(resp.End) != `{"Kind":1,"Message":"改写后"}` {
		t.Fatalf("End 应被改写：%s", resp.End)
	}
}

// 中间件能读到 Resumed 与 Turn 号。
func TestTurnChainSeesResumed(t *testing.T) {
	var got TurnReq
	p := regMw(t, "see", func(cx *Registrar) error {
		Wrap(cx, Turn, func(ctx context.Context, req TurnReq, next func(context.Context, TurnReq) (TurnResp, error)) (TurnResp, error) {
			got = req
			return next(ctx, req)
		})
		return nil
	})
	asm, err := Assemble(Spec{Plugins: entries(p)})
	if err != nil {
		t.Fatal(err)
	}
	kernel := func(ctx context.Context, req TurnReq) (TurnResp, error) { return TurnResp{}, nil }
	if _, err := asm.TurnChain(kernel)(context.Background(), TurnReq{SessionID: "s1", Turn: 3, Resumed: true}); err != nil {
		t.Fatal(err)
	}
	if got.Turn != 3 || !got.Resumed {
		t.Fatalf("应看到 Turn=3 Resumed=true：%+v", got)
	}
}

// 没有中间件时链就是内核。
func TestTurnChainEmpty(t *testing.T) {
	asm, err := Assemble(Spec{})
	if err != nil {
		t.Fatal(err)
	}
	if asm.HasMiddleware(PointTurnMw) {
		t.Fatal("没登记时不该有中间件")
	}
	called := false
	kernel := func(ctx context.Context, req TurnReq) (TurnResp, error) {
		called = true
		return TurnResp{}, nil
	}
	if _, err := asm.TurnChain(kernel)(context.Background(), TurnReq{}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("没中间件时应直接调内核")
	}
}
