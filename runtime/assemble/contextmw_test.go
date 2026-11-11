package assemble

// 04 4.3 Context 点位适配层：改写 Messages 生效、**改系统提示词无效**（限制 1）。

import (
	"context"
	"testing"

	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/session"
)

// ctxTestComponent 一个只跑 setup 的最小组件。
type ctxTestComponent struct {
	setup func(*component.Registrar) error
}

func (c *ctxTestComponent) Meta() component.Meta { return component.Meta{Name: "ctxmw-test"} }
func (c *ctxTestComponent) Setup(r *component.Registrar) error {
	if c.setup != nil {
		return c.setup(r)
	}
	return nil
}
func (c *ctxTestComponent) Start(*component.Scope) error { return nil }

// ctxTestAssembly 装一个登记了 Context 中间件的 Assembly。
func ctxTestAssembly(t *testing.T, setup func(*component.Registrar) error) *component.Assembly {
	t.Helper()
	name := t.Name() + "/ctxmw"
	component.Register(name, func() component.Component { return &ctxTestComponent{setup: setup} })
	asm, err := component.Assemble(component.Spec{Plugins: []component.Entry{{Name: name}}})
	if err != nil {
		t.Fatal(err)
	}
	return asm
}

// 中间件改 System 无效：适配层跑完链后还原内核版系统提示词。
func TestCtxMWRestoresSystem(t *testing.T) {
	asm := ctxTestAssembly(t, func(cx *component.Registrar) error {
		component.Wrap(cx, component.Context, func(ctx context.Context, req component.ContextReq,
			next func(context.Context, component.ContextReq) (component.ContextResp, error)) (component.ContextResp, error) {
			req.Request.System = []session.SystemBlock{{Text: "篡改"}}
			return next(ctx, req)
		})
		return nil
	})
	mw := ctxMiddleware(asm)
	if mw == nil {
		t.Fatal("应装到适配器")
	}
	req := &session.Request{System: []session.SystemBlock{{Text: "内核提示词"}}}
	out, err := mw.Build(context.Background(), req, func(ctx context.Context, r *session.Request) (*session.Request, error) {
		return r, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.System) != 1 || out.System[0].Text != "内核提示词" {
		t.Fatalf("系统提示词应被还原成内核版，实际 %+v", out.System)
	}
}

// 中间件改 Messages 生效。
func TestCtxMWKeepsMessages(t *testing.T) {
	asm := ctxTestAssembly(t, func(cx *component.Registrar) error {
		component.Wrap(cx, component.Context, func(ctx context.Context, req component.ContextReq,
			next func(context.Context, component.ContextReq) (component.ContextResp, error)) (component.ContextResp, error) {
			req.Request.Messages = append(req.Request.Messages, session.Message{Role: "user",
				Content: session.Content{session.NewTextPart("附加")}})
			return next(ctx, req)
		})
		return nil
	})
	mw := ctxMiddleware(asm)
	req := &session.Request{Messages: []session.Message{{Role: "user"}}}
	out, err := mw.Build(context.Background(), req, func(ctx context.Context, r *session.Request) (*session.Request, error) {
		return r, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) != 2 {
		t.Fatalf("Messages 改动应保留：%d 条", len(out.Messages))
	}
}

// 没登记中间件时适配器返回 nil（agent 直接走内核组装，零开销）。
func TestCtxMWNilWhenEmpty(t *testing.T) {
	asm, err := component.Assemble(component.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	if ctxMiddleware(asm) != nil {
		t.Fatal("没登记时适配器应为 nil")
	}
}
