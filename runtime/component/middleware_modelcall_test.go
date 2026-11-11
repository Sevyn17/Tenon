package component

// ModelCall 中间件点位（04 4.3）：顺序、脱敏、换模型、改写回复。

import (
	"context"
	"testing"

	"github.com/Sevyn17/tenon/runtime/session"
)

// 顺序：先登记在外层。
func TestModelCallChainOrder(t *testing.T) {
	var order []string
	pa := regMw(t, "ma", func(cx *Registrar) error {
		Wrap(cx, ModelCall, func(ctx context.Context, req ModelCallReq, next func(context.Context, ModelCallReq) (ModelCallResp, error)) (ModelCallResp, error) {
			order = append(order, "a-in")
			r, err := next(ctx, req)
			order = append(order, "a-out")
			return r, err
		})
		return nil
	})
	pb := regMw(t, "mb", func(cx *Registrar) error {
		Wrap(cx, ModelCall, func(ctx context.Context, req ModelCallReq, next func(context.Context, ModelCallReq) (ModelCallResp, error)) (ModelCallResp, error) {
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
	kernel := func(ctx context.Context, req ModelCallReq) (ModelCallResp, error) {
		order = append(order, "kernel")
		return ModelCallResp{}, nil
	}
	if _, err := asm.ModelCallChain(kernel)(context.Background(), ModelCallReq{Request: &session.Request{}}); err != nil {
		t.Fatal(err)
	}
	want := []string{"a-in", "b-in", "kernel", "b-out", "a-out"}
	for i := range want {
		if i >= len(order) || order[i] != want[i] {
			t.Fatalf("顺序 = %v，期望 %v", order, want)
		}
	}
}

// 中间件能脱敏（改 Messages）与换模型（改 Model）。
func TestModelCallChainRewrites(t *testing.T) {
	p := regMw(t, "redact", func(cx *Registrar) error {
		Wrap(cx, ModelCall, func(ctx context.Context, req ModelCallReq, next func(context.Context, ModelCallReq) (ModelCallResp, error)) (ModelCallResp, error) {
			req.Request.Model = "cheap-model" // 换模型（注意：会让前缀缓存失效）
			req.Request.Messages = []session.Message{{Role: "user",
				Content: session.Content{session.NewTextPart("[已脱敏]")}}}
			return next(ctx, req)
		})
		return nil
	})
	asm, err := Assemble(Spec{Plugins: entries(p)})
	if err != nil {
		t.Fatal(err)
	}
	var gotModel string
	var gotMsgs int
	kernel := func(ctx context.Context, req ModelCallReq) (ModelCallResp, error) {
		gotModel = req.Request.Model
		gotMsgs = len(req.Request.Messages)
		return ModelCallResp{}, nil
	}
	req := &session.Request{Model: "expensive", Messages: []session.Message{{Role: "user"}, {Role: "assistant"}}}
	if _, err := asm.ModelCallChain(kernel)(context.Background(), ModelCallReq{Request: req}); err != nil {
		t.Fatal(err)
	}
	if gotModel != "cheap-model" || gotMsgs != 1 {
		t.Fatalf("内核应看到换模型+脱敏后的请求：model=%q msgs=%d", gotModel, gotMsgs)
	}
}

// 中间件能改写回复（如清洗内容）。
func TestModelCallChainRewritesReply(t *testing.T) {
	p := regMw(t, "clean", func(cx *Registrar) error {
		Wrap(cx, ModelCall, func(ctx context.Context, req ModelCallReq, next func(context.Context, ModelCallReq) (ModelCallResp, error)) (ModelCallResp, error) {
			resp, err := next(ctx, req)
			if err != nil {
				return resp, err
			}
			resp.Reply.Content = session.Content{session.NewTextPart("清洗后")}
			return resp, nil
		})
		return nil
	})
	asm, err := Assemble(Spec{Plugins: entries(p)})
	if err != nil {
		t.Fatal(err)
	}
	kernel := func(ctx context.Context, req ModelCallReq) (ModelCallResp, error) {
		return ModelCallResp{Reply: session.AssistantItem{Content: session.Content{session.NewTextPart("原始")}}}, nil
	}
	resp, err := asm.ModelCallChain(kernel)(context.Background(), ModelCallReq{Request: &session.Request{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Reply.Content) == 0 || resp.Reply.Content[0].Text != "清洗后" {
		t.Fatalf("回复应被改写：%+v", resp.Reply.Content)
	}
}

// 中间件能读到 Retried（上下文超限重发）。
func TestModelCallChainSeesRetried(t *testing.T) {
	var got bool
	p := regMw(t, "see", func(cx *Registrar) error {
		Wrap(cx, ModelCall, func(ctx context.Context, req ModelCallReq, next func(context.Context, ModelCallReq) (ModelCallResp, error)) (ModelCallResp, error) {
			got = req.Retried
			return next(ctx, req)
		})
		return nil
	})
	asm, err := Assemble(Spec{Plugins: entries(p)})
	if err != nil {
		t.Fatal(err)
	}
	kernel := func(ctx context.Context, req ModelCallReq) (ModelCallResp, error) { return ModelCallResp{}, nil }
	if _, err := asm.ModelCallChain(kernel)(context.Background(), ModelCallReq{Request: &session.Request{}, Retried: true}); err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Fatal("应看到 Retried=true")
	}
}

// 没有中间件时链就是内核。
func TestModelCallChainEmpty(t *testing.T) {
	asm, err := Assemble(Spec{})
	if err != nil {
		t.Fatal(err)
	}
	if asm.HasMiddleware(PointModelCallMw) {
		t.Fatal("没登记时不该有中间件")
	}
	called := false
	kernel := func(ctx context.Context, req ModelCallReq) (ModelCallResp, error) {
		called = true
		return ModelCallResp{}, nil
	}
	if _, err := asm.ModelCallChain(kernel)(context.Background(), ModelCallReq{Request: &session.Request{}}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("没中间件时应直接调内核")
	}
}
