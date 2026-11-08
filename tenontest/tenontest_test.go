package tenontest_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Sevyn17/tenon"
	"github.com/Sevyn17/tenon/runtime/assemble"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/toolhub"
	"github.com/Sevyn17/tenon/tenontest"
)

// echo 测试组件：登记一个无需授权的只读工具。
type echo struct{}

func (e *echo) Name() string { return "echo-test" }

func (e *echo) Setup(cx *component.Registrar) error {
	tl := &toolhub.Tool{
		Spec: session.ToolSpec{
			Name:        "echo",
			Description: "回显",
			Schema:      json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`),
		},
		Risk:   toolhub.Read,
		NoAuth: true,
		Call: func(ctx context.Context, cx *toolhub.Cx, args json.RawMessage) (toolhub.Output, error) {
			return toolhub.Text("ok"), nil
		},
	}
	cx.Register(assemble.PointSessionTools, "echo-test", assemble.ToolsEntry{
		Tools:   func() []*toolhub.Tool { return []*toolhub.Tool{tl} },
		Visible: func(*assemble.SessionCx) bool { return true },
	})
	return nil
}

func (e *echo) Start(*component.Scope) error { return nil }

// TestAskWithTool 端到端：脚本模型 → 调工具 → 再回复（10 10.3 的最小验收）。
// v1 验收第 1 条：tenontest 写的验收用例在两种存储上都过。
func TestAskWithTool(t *testing.T) {
	for _, b := range tenontest.Stores() {
		t.Run(b.Name, func(t *testing.T) { askWithTool(t, b) })
	}
}

func askWithTool(t *testing.T, b tenontest.Backend) {
	app := tenontest.NewOn(t, b.Open(t),
		tenontest.Model(
			tenontest.Reply("先查").CallTool("echo", `{"text":"hi"}`),
			tenontest.Reply("好了"),
		),
		tenon.With(&echo{}),
	)
	reply, err := app.Ask(context.Background(), "subagent", "hi", nil)
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if reply.Text != "好了" {
		t.Fatalf("回复文本 = %q，要 好了", reply.Text)
	}
	if reply.SessionID == "" {
		t.Fatal("会话 id 为空")
	}
	app.AssertToolCalled("echo")
}

// TestAskPlainReply 不需要工具的一轮。
func TestAskPlainReply(t *testing.T) {
	for _, b := range tenontest.Stores() {
		t.Run(b.Name, func(t *testing.T) { askPlainReply(t, b) })
	}
}

func askPlainReply(t *testing.T, b tenontest.Backend) {
	app := tenontest.NewOn(t, b.Open(t), tenontest.Model(tenontest.Reply("你好！")))
	reply, err := app.Ask(context.Background(), "subagent", "你好", nil)
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if reply.Text != "你好！" {
		t.Fatalf("回复文本 = %q", reply.Text)
	}
}
