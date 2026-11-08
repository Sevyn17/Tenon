package appapi

import (
	"context"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/runtime/assemble"
	"github.com/Sevyn17/tenon/runtime/bus"
)

// 带会话的提问：ask 事件带 session，界面按会话路由（2026-11-04 审查 P1-28）。
func TestAskInCarriesSession(t *testing.T) {
	b := bus.New(0)
	defer b.Close()
	ui := NewUI(b)
	var _ assemble.SessionUI = ui
	sub := b.Subscribe(context.Background(), bus.Filter{Kinds: []string{EventAsk}})
	go func() { _, _ = assemble.AskIn(ui, context.Background(), "s-7", "继续吗？", nil) }()
	select {
	case ev := <-sub.C():
		if ev.Session != "s-7" {
			t.Fatalf("ask 事件应带会话 s-7，得 %q", ev.Session)
		}
		m, _ := ev.Data.(map[string]any)
		id, _ := m["ask_id"].(string)
		if err := ui.Answer(id, "是"); err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("没收到 ask 事件")
	}
}
