package assemble

// extproc 从「会话层」迁到「ToolCall 点位」后，中间件确实被装进链（04 4.3.1 第 1 步）。

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// 登记在 ToolCall 点位的中间件会出现在 ToolCallLayers 里，并且真的包住调用。
func TestToolCallPointReachesLayers(t *testing.T) {
	asm := ctxTestAssembly(t, func(cx *component.Registrar) error {
		component.Wrap(cx, component.ToolCall, func(ctx context.Context, req component.ToolCallReq,
			next func(context.Context, component.ToolCallReq) (component.ToolCallResp, error)) (component.ToolCallResp, error) {
			if req.Tool == "blocked" {
				return component.ToolCallResp{Denied: true}, nil
			}
			return next(ctx, req)
		})
		return nil
	})
	if !asm.HasMiddleware(component.PointToolCallMw) {
		t.Fatal("ToolCall 点位应有中间件")
	}
	layers := asm.ToolCallLayers()
	if len(layers) != 1 {
		t.Fatalf("应有一层，实际 %d", len(layers))
	}
	// 被拒：工具不执行。
	ran := false
	next := toolhub.Next(func(ctx context.Context, cx *toolhub.Cx, args json.RawMessage) (toolhub.Output, error) {
		ran = true
		return toolhub.Text("ok"), nil
	})
	cx := &toolhub.Cx{Tool: &toolhub.Tool{Spec: session.ToolSpec{Name: "blocked"}}}
	out, err := layers[0].Call(context.Background(), cx, nil, next)
	if err != nil {
		t.Fatal(err)
	}
	if ran || out.Status != session.ToolError {
		t.Fatalf("blocked 应被拦截：ran=%v out=%+v", ran, out)
	}
	// 放行：工具执行。
	cx2 := &toolhub.Cx{Tool: &toolhub.Tool{Spec: session.ToolSpec{Name: "ok"}}}
	if _, err := layers[0].Call(context.Background(), cx2, nil, next); err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Fatal("ok 应放行")
	}
}
