package events_test

// 定义 + [plugin.events] 装配：组件装配通过，SessionTools 点位有 events 登记（含 event_dispatch 工具）。

import (
	"testing"

	"github.com/Sevyn17/tenon"
	"github.com/Sevyn17/tenon/tenontest"
)

func TestEventsWiring(t *testing.T) {
	app := tenontest.New(t,
		tenon.WithConfig("permission", map[string]any{"force_mode": "full"}),
		tenon.WithConfig("events", map[string]any{
			"schedule": []any{map[string]any{"every": "1h", "target": "resident:日报", "body": "汇总"}},
		}),
		tenon.WithAgent(
			tenon.Agent{Name: "router", Prompt: "分发"},
			tenon.Agent{Name: "worker", Prompt: "处理"},
		),
		tenontest.Model(tenontest.Reply("ok")))
	asm := app.Internals().Assembly
	var hasEvents bool
	for _, it := range asm.Items("session_tools") {
		if it.Name == "events" {
			hasEvents = true
		}
	}
	if !hasEvents {
		t.Fatal("SessionTools 应登记 events（event_dispatch 工具）")
	}
}
