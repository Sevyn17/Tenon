package tenon_test

// T4e 小组模块接线验收：两个定义 + 一段 [use.group] 配置拼出群，组件装配通过、资源类型 group 已登记、
// 背景注入点位已挂上（不写业务 Go 代码）。

import (
	"context"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon"
	"github.com/Sevyn17/tenon/runtime/store/memstore"
	"github.com/Sevyn17/tenon/tenontest"
)

func TestGroupModuleWiring(t *testing.T) {
	app := tenontest.New(t,
		tenon.WithAgent(
			tenon.Agent{Name: "alice-bot", Prompt: "你是订单助手"},
			tenon.Agent{Name: "bob-bot", Prompt: "你是报表助手"},
		),
		tenon.WithConfig("group", map[string]any{
			"groups": []any{
				map[string]any{"id": "g1", "bots": []any{"alice-bot", "bob-bot"}},
			},
		}),
		tenontest.Model(tenontest.Reply("在")))
	_ = app

	asm := app.Internals().Assembly
	var hasGroup bool
	for _, rt := range asm.ResourceTypes() {
		if rt.Name == "group" {
			hasGroup = true
		}
	}
	if !hasGroup {
		t.Fatal("group 资源类型应已登记")
	}
	// explain 应能看到 group 组件与会话点位（背景注入 / afterTurn）。
	var b strings.Builder
	asm.Explain(&b, nil)
	if !strings.Contains(b.String(), "group") {
		t.Fatalf("explain 应含 group 组件：\n%s", b.String())
	}
}

// WithAgent 里的两个定义可解析、群资源类型登记在装配里（配置没写错时 New 成功）。
func TestGroupConfigAccepted(t *testing.T) {
	_, err := tenon.New(
		tenon.Default(), tenon.WithStore(memstore.MemoryStore()), tenon.WithWorkdir(t.TempDir()),
		tenon.WithAgent(tenon.Agent{Name: "alice-bot"}, tenon.Agent{Name: "bob-bot"}),
		tenon.WithConfig("group", map[string]any{
			"manual": false,
			"groups": []any{map[string]any{"id": "g1", "bots": []any{"alice-bot", "bob-bot"}}},
		}),
		tenontest.Model())
	if err != nil {
		t.Fatalf("带 [use.group] 配置应能装配：%v", err)
	}
	_ = context.Background
}
