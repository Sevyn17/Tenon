package component

// ToolCall 中间件点位（04 4.3）：拦截、改参数、改写结果、装在最外层。

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// 中间件能拦截（Denied → 工具不执行）。
func TestToolCallLayerDenies(t *testing.T) {
	p := regMw(t, "deny", func(cx *Registrar) error {
		Wrap(cx, ToolCall, func(ctx context.Context, req ToolCallReq, next func(context.Context, ToolCallReq) (ToolCallResp, error)) (ToolCallResp, error) {
			if req.Tool == "danger" {
				return ToolCallResp{Denied: true}, nil
			}
			return next(ctx, req)
		})
		return nil
	})
	asm, err := Assemble(Spec{Plugins: entries(p)})
	if err != nil {
		t.Fatal(err)
	}
	layers := asm.ToolCallTransforms()
	if len(layers) != 1 {
		t.Fatalf("应有一层：%d", len(layers))
	}
	// 直接跑这一层：danger 被拒，其他放行。
	called := false
	next := func(ctx context.Context, cx *toolhub.Cx, args json.RawMessage) (toolhub.Output, error) {
		called = true
		return toolhub.Text("ok"), nil
	}
	cx := &toolhub.Cx{Tool: &toolhub.Tool{Spec: sessionToolSpec("danger")}}
	out, err := layers[0].Call(context.Background(), cx, nil, next)
	if err != nil {
		t.Fatal(err)
	}
	if called || out.Status != session.ToolError {
		t.Fatalf("danger 应被拦截：called=%v out=%+v", called, out)
	}
	// 普通工具放行。
	cx2 := &toolhub.Cx{Tool: &toolhub.Tool{Spec: sessionToolSpec("safe")}}
	if _, err := layers[0].Call(context.Background(), cx2, nil, next); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("safe 应放行")
	}
}

// 中间件能改写参数，改写后的参数传给下一层。
func TestToolCallLayerRewritesArgs(t *testing.T) {
	p := regMw(t, "rw", func(cx *Registrar) error {
		Wrap(cx, ToolCall, func(ctx context.Context, req ToolCallReq, next func(context.Context, ToolCallReq) (ToolCallResp, error)) (ToolCallResp, error) {
			req.Args = json.RawMessage(`{"patched":true}`)
			return next(ctx, req)
		})
		return nil
	})
	asm, err := Assemble(Spec{Plugins: entries(p)})
	if err != nil {
		t.Fatal(err)
	}
	var seen string
	next := func(ctx context.Context, cx *toolhub.Cx, args json.RawMessage) (toolhub.Output, error) {
		seen = string(args)
		return toolhub.Text("ok"), nil
	}
	cx := &toolhub.Cx{Tool: &toolhub.Tool{Spec: sessionToolSpec("x")}}
	if _, err := asm.ToolCallTransforms()[0].Call(context.Background(), cx, json.RawMessage(`{"a":1}`), next); err != nil {
		t.Fatal(err)
	}
	if seen != `{"patched":true}` {
		t.Fatalf("下一层应看到改写后的参数，实际 %q", seen)
	}
}

// 中间件能改写结果。
func TestToolCallLayerRewritesOutput(t *testing.T) {
	p := regMw(t, "redact", func(cx *Registrar) error {
		Wrap(cx, ToolCall, func(ctx context.Context, req ToolCallReq, next func(context.Context, ToolCallReq) (ToolCallResp, error)) (ToolCallResp, error) {
			resp, err := next(ctx, req)
			if err != nil {
				return resp, err
			}
			resp.Output = toolhub.Text("redacted")
			return resp, nil
		})
		return nil
	})
	asm, err := Assemble(Spec{Plugins: entries(p)})
	if err != nil {
		t.Fatal(err)
	}
	next := func(ctx context.Context, cx *toolhub.Cx, args json.RawMessage) (toolhub.Output, error) {
		return toolhub.Text("secret"), nil
	}
	cx := &toolhub.Cx{Tool: &toolhub.Tool{Spec: sessionToolSpec("x")}}
	out, err := asm.ToolCallTransforms()[0].Call(context.Background(), cx, nil, next)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Content) == 0 || out.Content[0].Text != "redacted" {
		t.Fatalf("结果应被改写：%+v", out)
	}
}

// 没有中间件时 ToolCallTransforms 返回 nil（不装任何层）。
func TestToolCallTransformsEmpty(t *testing.T) {
	asm, err := Assemble(Spec{})
	if err != nil {
		t.Fatal(err)
	}
	if got := asm.ToolCallTransforms(); len(got) != 0 {
		t.Fatalf("没登记时应为 nil：%+v", got)
	}
}

// sessionToolSpec 造一个最小工具声明（测试用）。
func sessionToolSpec(name string) session.ToolSpec { return session.ToolSpec{Name: name} }
