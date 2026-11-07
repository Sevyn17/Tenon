package tenon_test

import (
	"context"
	"errors"
	"testing"

	"tenon"
	"tenon/runtime/component"
	"tenon/runtime/host"
)

// denyOpen 应用组件：拒绝一切新建会话（会话打开守卫）。
type denyOpen struct{}

func (denyOpen) Name() string { return "deny-open" }
func (denyOpen) Setup(cx *component.Registrar) error {
	cx.Guard(component.GuardRule{Name: "deny-open", On: component.GuardSessionOpen,
		Check: func(component.GuardRequest) (component.Verdict, string) { return component.Deny, "测试拒绝" }})
	return nil
}
func (denyOpen) Start(*component.Scope) error { return nil }

// Ask 走信封投递，经过会话打开守卫（审查 P1-24：原先 Ask 自己建会话，守卫拦不住）。
func TestAskGoesThroughGuards(t *testing.T) {
	dir := t.TempDir()
	writeAgent(t, dir, "support", "format = \"tenon/v1\"\nid = \"support\"\n")
	opts := append(base(t), tenon.WithAgents(tenon.Dir(dir)), tenon.With(denyOpen{}))
	app, err := tenon.New(opts...)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	defer app.Close(ctx)
	if err := app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Ask(ctx, "support", "hi", nil); !errors.Is(err, host.ErrNotAllowed) {
		t.Fatalf("守卫拒绝新建会话时 Ask 应失败：%v", err)
	}
}

// 定义不存在时 Ask 返回可判的 ErrNotFound。
func TestAskUnknownDefinition(t *testing.T) {
	app, err := tenon.New(base(t)...)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	defer app.Close(ctx)
	if err := app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Ask(ctx, "nope", "hi", nil); !errors.Is(err, tenon.ErrNotFound) {
		t.Fatalf("未知定义应 ErrNotFound：%v", err)
	}
}
