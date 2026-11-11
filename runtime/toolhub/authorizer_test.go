package toolhub

// 授权判定者（04 4.3.1）：Authorizer 替代 Authorizes 层，判定在最内层（紧贴执行器）。

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Sevyn17/tenon/runtime/session"
)

// 造一个带 Authorizer 的 Hub（工具 + 执行计数）。
func authHub(t *testing.T, a Authorizer) (*Hub, *int) {
	t.Helper()
	h := New(Options{})
	ran := 0
	if err := h.Register(&Tool{Spec: session.ToolSpec{Name: "w"}, Risk: Write, NoAuth: true,
		Call: func(context.Context, *Cx, json.RawMessage) (Output, error) {
			ran++
			return Text("done"), nil
		}}); err != nil {
		t.Fatal(err)
	}
	h.SetAuthorizer(a)
	return h, &ran
}

// Allow → 工具执行。
func TestAuthorizerAllow(t *testing.T) {
	h, ran := authHub(t, AuthorizerFunc(func(ctx context.Context, cx *Cx, args json.RawMessage) (Decision, error) {
		return Decision{Action: DecisionAllow}, nil
	}))
	if err := h.Validate(); err != nil {
		t.Fatalf("设了 Authorizer 应通过校验：%v", err)
	}
	run := h.Execute(context.Background(), nil, []session.ToolCall{{ID: "c1", Name: "w", Args: json.RawMessage(`{}`)}}, nil)
	res, _ := run.Next()
	if res.Output.Status != session.ToolOK || *ran != 1 {
		t.Fatalf("应放行并执行：%+v ran=%d", res.Output, *ran)
	}
}

// Deny → 不执行，返回错误结果。
func TestAuthorizerDeny(t *testing.T) {
	h, ran := authHub(t, AuthorizerFunc(func(ctx context.Context, cx *Cx, args json.RawMessage) (Decision, error) {
		return Decision{Action: DecisionDeny, Reason: "不许写"}, nil
	}))
	run := h.Execute(context.Background(), nil, []session.ToolCall{{ID: "c1", Name: "w", Args: json.RawMessage(`{}`)}}, nil)
	res, _ := run.Next()
	if res.Output.Status != session.ToolError || *ran != 0 {
		t.Fatalf("应拒绝且不执行：%+v ran=%d", res.Output, *ran)
	}
}

// 判定失败（error）→ 失败即收紧，不执行。
func TestAuthorizerErrorFailsClosed(t *testing.T) {
	h, ran := authHub(t, AuthorizerFunc(func(ctx context.Context, cx *Cx, args json.RawMessage) (Decision, error) {
		return Decision{}, context.DeadlineExceeded
	}))
	run := h.Execute(context.Background(), nil, []session.ToolCall{{ID: "c1", Name: "w", Args: json.RawMessage(`{}`)}}, nil)
	res, _ := run.Next()
	if res.Output.Status != session.ToolError || *ran != 0 {
		t.Fatalf("判定失败应拒绝执行：%+v ran=%d", res.Output, *ran)
	}
}

// 已批准（cx.Approved）时 Confirm 直接放行。
func TestAuthorizerConfirmApproved(t *testing.T) {
	h, ran := authHub(t, AuthorizerFunc(func(ctx context.Context, cx *Cx, args json.RawMessage) (Decision, error) {
		if cx.Approved {
			return Decision{Action: DecisionAllow}, nil
		}
		return Decision{Action: DecisionConfirm, Reason: "要确认"}, nil
	}))
	// 不批准：应进确认流程（Await 未设 → 按拒绝/超时结清）。
	run := h.Execute(context.Background(), nil, []session.ToolCall{{ID: "c1", Name: "w", Args: json.RawMessage(`{}`)}}, nil)
	res, _ := run.Next()
	if res.Output.Status != session.ToolError {
		t.Fatalf("未批准时应以失败结清：%+v", res.Output)
	}
	if *ran != 0 {
		t.Fatalf("未批准不该执行：ran=%d", *ran)
	}
}

// 两者都没有 → 装配拒绝（失败即收紧）。
func TestNoAuthorizerNoLayerRejected(t *testing.T) {
	h := New(Options{})
	if err := h.Validate(); err == nil {
		t.Fatal("没有授权落点应拒绝装配")
	}
}

// Authorizer 拿到的是改写层之后的最终参数（判定在链最内层）。
func TestAuthorizerSeesRewrittenArgs(t *testing.T) {
	h := New(Options{})
	var seen string
	if err := h.Register(&Tool{Spec: session.ToolSpec{Name: "w"}, Risk: Write, NoAuth: true,
		Call: func(context.Context, *Cx, json.RawMessage) (Output, error) { return Text("done"), nil }}); err != nil {
		t.Fatal(err)
	}
	// 一个改写层：把参数换成 patched。
	h.AddLayer(Layer{Name: "rewrite", Call: func(ctx context.Context, cx *Cx, args json.RawMessage, next Next) (Output, error) {
		return next(ctx, cx, json.RawMessage(`{"patched":true}`))
	}})
	h.SetAuthorizer(AuthorizerFunc(func(ctx context.Context, cx *Cx, args json.RawMessage) (Decision, error) {
		seen = string(args)
		return Decision{Action: DecisionAllow}, nil
	}))
	run := h.Execute(context.Background(), nil, []session.ToolCall{{ID: "c1", Name: "w", Args: json.RawMessage(`{"orig":1}`)}}, nil)
	_, _ = run.Next()
	if seen != `{"patched":true}` {
		t.Fatalf("Authorizer 应看到改写后的参数，实际 %q", seen)
	}
}
