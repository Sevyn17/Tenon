package assemble

// 04 4.3 ModelCall 点位适配层：中间件能改请求与回复，没登记时零开销。

import (
	"context"
	"sync"
	"testing"

	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/session"
)

// 中间件能改请求（换模型 / 脱敏）与回复。
func TestModelMWAdapts(t *testing.T) {
	asm := ctxTestAssembly(t, func(cx *component.Registrar) error {
		component.Wrap(cx, component.ModelCall, func(ctx context.Context, req component.ModelCallReq,
			next func(context.Context, component.ModelCallReq) (component.ModelCallResp, error)) (component.ModelCallResp, error) {
			req.Request.Model = "cheap"
			resp, err := next(ctx, req)
			if err != nil {
				return resp, err
			}
			resp.Reply.Content = session.Content{session.NewTextPart("改写")}
			return resp, nil
		})
		return nil
	})
	mw := modelMiddleware(asm)
	if mw == nil {
		t.Fatal("应装到适配器")
	}
	var seenModel string
	req := &session.Request{Model: "expensive"}
	reply, _, err := mw.Call(context.Background(), req, func(ctx context.Context, r *session.Request) (session.AssistantItem, session.StopReason, error) {
		seenModel = r.Model
		return session.AssistantItem{Content: session.Content{session.NewTextPart("原始")}}, session.StopReason{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seenModel != "cheap" {
		t.Fatalf("内核应看到换过的模型：%q", seenModel)
	}
	if len(reply.Content) == 0 || reply.Content[0].Text != "改写" {
		t.Fatalf("回复应被改写：%+v", reply.Content)
	}
}

// 中间件返回错误按正常错误传回。
func TestModelMWError(t *testing.T) {
	boom := context.Canceled
	asm := ctxTestAssembly(t, func(cx *component.Registrar) error {
		component.Wrap(cx, component.ModelCall, func(ctx context.Context, req component.ModelCallReq,
			next func(context.Context, component.ModelCallReq) (component.ModelCallResp, error)) (component.ModelCallResp, error) {
			return component.ModelCallResp{}, boom
		})
		return nil
	})
	mw := modelMiddleware(asm)
	_, _, err := mw.Call(context.Background(), &session.Request{}, func(ctx context.Context, r *session.Request) (session.AssistantItem, session.StopReason, error) {
		return session.AssistantItem{}, session.StopReason{}, nil
	})
	if err != boom {
		t.Fatalf("错误应传回：%v", err)
	}
}

// 没登记中间件时适配器返回 nil。
func TestModelMWNilWhenEmpty(t *testing.T) {
	asm, err := component.Assemble(component.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	if modelMiddleware(asm) != nil {
		t.Fatal("没登记时适配器应为 nil")
	}
}

// 并发调用：链本身无共享可变状态，多个会话同时跑不互相干扰。
func TestModelMWConcurrent(t *testing.T) {
	asm := ctxTestAssembly(t, func(cx *component.Registrar) error {
		component.Wrap(cx, component.ModelCall, func(ctx context.Context, req component.ModelCallReq,
			next func(context.Context, component.ModelCallReq) (component.ModelCallResp, error)) (component.ModelCallResp, error) {
			return next(ctx, req)
		})
		return nil
	})
	mw := modelMiddleware(asm)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _ = mw.Call(context.Background(), &session.Request{Model: "m"},
				func(ctx context.Context, r *session.Request) (session.AssistantItem, session.StopReason, error) {
					return session.AssistantItem{}, session.StopReason{}, nil
				})
		}()
	}
	wg.Wait()
}
