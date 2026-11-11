package host

// 04 4.3 ToolCall 中间件与守卫的层顺序：中间件在外层，守卫在内层，改写后的调用仍过守卫。

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// 中间件拦截：工具不执行。
func TestToolCallMWDeniesExecution(t *testing.T) {
	hub := toolhub.New(toolhub.Options{})
	ran := 0
	tool := &toolhub.Tool{Spec: session.ToolSpec{Name: "echo"},
		Risk: toolhub.Read, NoAuth: true,
		Call: func(context.Context, *toolhub.Cx, json.RawMessage) (toolhub.Output, error) {
			ran++
			return toolhub.Text("done"), nil
		}}
	if err := hub.Register(tool); err != nil {
		t.Fatal(err)
	}
	hub.PrependLayer(toolhub.Layer{Name: "mw", Guard: false,
		Call: func(ctx context.Context, cx *toolhub.Cx, args json.RawMessage, next toolhub.Next) (toolhub.Output, error) {
			return toolhub.Failf("被中间件拦截"), nil
		}})
	run := hub.Execute(context.Background(), nil, []session.ToolCall{{ID: "c1", Name: "echo", Args: json.RawMessage(`{}`)}}, nil)
	res, _ := run.Next()
	if res.Output.Status != session.ToolError {
		t.Fatalf("应被拦成失败：%+v", res.Output)
	}
	if ran != 0 {
		t.Fatalf("工具不该执行：ran=%d", ran)
	}
}

// 层顺序：中间件在外、守卫在内。守卫拒绝时工具不执行，中间件能看到拒绝结果。
func TestToolCallMWOutsideGuard(t *testing.T) {
	hub := toolhub.New(toolhub.Options{})
	ran := 0
	tool := &toolhub.Tool{Spec: session.ToolSpec{Name: "w"},
		Risk: toolhub.Write, NoAuth: true,
		Call: func(context.Context, *toolhub.Cx, json.RawMessage) (toolhub.Output, error) {
			ran++
			return toolhub.Text("done"), nil
		}}
	if err := hub.Register(tool); err != nil {
		t.Fatal(err)
	}
	// 内层守卫：一律拒绝。
	hub.AddLayer(toolhub.Layer{Name: "guard", Guard: true,
		Call: func(ctx context.Context, cx *toolhub.Cx, args json.RawMessage, next toolhub.Next) (toolhub.Output, error) {
			return toolhub.Failf("守卫拒绝"), nil
		}})
	// 外层中间件：能看到拒绝结果。
	sawDeny := false
	hub.PrependLayer(toolhub.Layer{Name: "mw", Guard: false,
		Call: func(ctx context.Context, cx *toolhub.Cx, args json.RawMessage, next toolhub.Next) (toolhub.Output, error) {
			out, err := next(ctx, cx, args)
			if out.Status == session.ToolError {
				sawDeny = true
			}
			return out, err
		}})
	run := hub.Execute(context.Background(), nil, []session.ToolCall{{ID: "c1", Name: "w", Args: json.RawMessage(`{}`)}}, nil)
	res, _ := run.Next()
	if res.Output.Status != session.ToolError {
		t.Fatalf("守卫应拒绝：%+v", res.Output)
	}
	if ran != 0 {
		t.Fatal("守卫拒绝后工具不该执行")
	}
	if !sawDeny {
		t.Fatal("外层中间件应看到守卫的拒绝结果")
	}
}

// 中间件改写参数后，内层守卫看到的是改写后的参数（不能靠改写绕过判定）。
func TestToolCallMWRewriteSeenByGuard(t *testing.T) {
	hub := toolhub.New(toolhub.Options{})
	var guardArgs string
	tool := &toolhub.Tool{Spec: session.ToolSpec{Name: "w"},
		Risk: toolhub.Write, NoAuth: true,
		Call: func(context.Context, *toolhub.Cx, json.RawMessage) (toolhub.Output, error) {
			return toolhub.Text("done"), nil
		}}
	if err := hub.Register(tool); err != nil {
		t.Fatal(err)
	}
	hub.AddLayer(toolhub.Layer{Name: "guard", Guard: true,
		Call: func(ctx context.Context, cx *toolhub.Cx, args json.RawMessage, next toolhub.Next) (toolhub.Output, error) {
			guardArgs = string(args)
			return next(ctx, cx, args)
		}})
	hub.PrependLayer(toolhub.Layer{Name: "mw", Guard: false,
		Call: func(ctx context.Context, cx *toolhub.Cx, args json.RawMessage, next toolhub.Next) (toolhub.Output, error) {
			return next(ctx, cx, json.RawMessage(`{"rewritten":true}`))
		}})
	run := hub.Execute(context.Background(), nil, []session.ToolCall{{ID: "c1", Name: "w", Args: json.RawMessage(`{"orig":1}`)}}, nil)
	_, _ = run.Next()
	if guardArgs != `{"rewritten":true}` {
		t.Fatalf("守卫应看到中间件改写后的参数，实际 %q", guardArgs)
	}
}
