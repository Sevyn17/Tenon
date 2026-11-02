package ybasetest_test

import (
	"context"
	"encoding/json"
	"testing"

	"ybase-kernel/apps/ybase-sdk"
	"ybase-kernel/apps/ybasetest"
	"ybase-kernel/core/plugin"
	kr "ybase-kernel/core/runtime"
	"ybase-kernel/core/session"
	"ybase-kernel/core/toolhub"
)

// echo 测试组件：登记一个无需授权的只读工具。
type echo struct{}

func (e *echo) Name() string { return "echo-test" }

func (e *echo) Setup(cx *plugin.Registrar) error {
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
	cx.Register(kr.PointSessionTools, "echo-test", kr.ToolsEntry{
		Tools:   func() []*toolhub.Tool { return []*toolhub.Tool{tl} },
		Visible: func(*kr.SessionCx) bool { return true },
	})
	return nil
}

func (e *echo) Start(*plugin.Scope) error { return nil }

// TestAskWithTool 端到端：脚本模型 → 调工具 → 再回复（10 10.3 的最小验收）。
func TestAskWithTool(t *testing.T) {
	app := ybasetest.New(t,
		ybasetest.Model(
			ybasetest.Reply("先查").CallTool("echo", `{"text":"hi"}`),
			ybasetest.Reply("好了"),
		),
		ybase.With(&echo{}),
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
	app := ybasetest.New(t, ybasetest.Model(ybasetest.Reply("你好！")))
	reply, err := app.Ask(context.Background(), "subagent", "你好", nil)
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if reply.Text != "你好！" {
		t.Fatalf("回复文本 = %q", reply.Text)
	}
}
