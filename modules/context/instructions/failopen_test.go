package instructions

// 04 4.3.1 第 3 步：instructions 从「会话层」迁到 ToolCall 中间件后，
// 原 Layer 的 Timeout: 2s + FailOpen: true 语义要保住——发现指令超时就放行，不阻断工具调用。

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// discoverWithTimeout 超时返回空串（= 放行）。
func TestDiscoverWithTimeoutFailsOpen(t *testing.T) {
	in := NewInstructions(t.TempDir())
	// 把超时调到 10ms、发现耗时 50ms：一定超时 → 放行。
	in.discoverTimeout = 10 * time.Millisecond
	in.discoverHook = func(string) string {
		time.Sleep(50 * time.Millisecond)
		return "迟到的指令"
	}
	got := in.discoverWithTimeout(context.Background(), ".")
	if got != "" {
		t.Fatalf("超时应返回空串（放行），实际 %q", got)
	}
}

// 正常（不超时）时返回指令。
func TestDiscoverWithTimeoutReturnsText(t *testing.T) {
	in := NewInstructions(t.TempDir())
	in.discoverHook = func(string) string { return "项目指令" }
	if got := in.discoverWithTimeout(context.Background(), "."); got != "项目指令" {
		t.Fatalf("应返回指令，实际 %q", got)
	}
}

// 中间件对「非 agentsTools 工具」不介入（原 Layer 的 cx.Tool.Spec.Name 判断）。
func TestMiddlewareSkipsOtherTools(t *testing.T) {
	in := NewInstructions(t.TempDir())
	in.discoverHook = func(string) string { return "不该被追加" }
	mw := in.Middleware()
	resp, err := mw(context.Background(), component.ToolCallReq{Tool: "some_other_tool"},
		func(ctx context.Context, req component.ToolCallReq) (component.ToolCallResp, error) {
			return component.ToolCallResp{Output: toolhub.Text("done")}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Output.Content) != 1 {
		t.Fatalf("非 agentsTools 不该追加：%+v", resp.Output.Content)
	}
}

// 中间件对 agentsTools 工具追加指令（原 Layer 的行为）。
func TestMiddlewareAppendsForAgentsTools(t *testing.T) {
	in := NewInstructions(t.TempDir())
	in.discoverHook = func(string) string { return "项目指令" }
	mw := in.Middleware()
	resp, err := mw(context.Background(), component.ToolCallReq{Tool: "read_file",
		Args: json.RawMessage(`{"path":"sub/x.go"}`)},
		func(ctx context.Context, req component.ToolCallReq) (component.ToolCallResp, error) {
			return component.ToolCallResp{Output: toolhub.Text("done")}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Output.Content) < 2 {
		t.Fatalf("应追加指令：%+v", resp.Output.Content)
	}
	last := resp.Output.Content[len(resp.Output.Content)-1].Text
	if last == "" || !contains(last, "<project-instructions>") {
		t.Fatalf("追加内容应含 project-instructions 标记：%q", last)
	}
	_ = session.ToolOK
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
