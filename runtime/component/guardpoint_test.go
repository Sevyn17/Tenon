package component

// 判定区原语（04 4.3.1）：纯谓词并行 reduce、任一拒绝即拒、拒绝原因稳定、Authorizer 唯一。

import (
	"context"
	"testing"
)

// 全 Abstain → Abstain。
func TestEvalPointAllAbstain(t *testing.T) {
	p := regMw(t, "g1", func(cx *Registrar) error {
		WrapGuard(cx, ToolCall, "g1", func(ctx context.Context, req ToolCallReq) (Verdict, string) {
			return Abstain, ""
		})
		return nil
	})
	asm, err := Assemble(Spec{Plugins: entries(p)})
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := EvalPoint(asm, PointToolCallMw, context.Background(), ToolCallReq{}); v != Abstain {
		t.Fatalf("应 Abstain，实际 %v", v)
	}
}

// 任一 Deny 即拒。
func TestEvalPointAnyDeny(t *testing.T) {
	p := regMw(t, "g2", func(cx *Registrar) error {
		WrapGuard(cx, ToolCall, "ok", func(ctx context.Context, req ToolCallReq) (Verdict, string) {
			return Allow, ""
		})
		WrapGuard(cx, ToolCall, "no", func(ctx context.Context, req ToolCallReq) (Verdict, string) {
			return Deny, "工具不允许"
		})
		WrapGuard(cx, ToolCall, "abstain", func(ctx context.Context, req ToolCallReq) (Verdict, string) {
			return Abstain, ""
		})
		return nil
	})
	asm, err := Assemble(Spec{Plugins: entries(p)})
	if err != nil {
		t.Fatal(err)
	}
	v, why := EvalPoint(asm, PointToolCallMw, context.Background(), ToolCallReq{})
	if v != Deny || why != "工具不允许" {
		t.Fatalf("应 Deny 且带原因：%v %q", v, why)
	}
}

// 多条 Deny：取登记顺序最靠前的原因（确定性）。
func TestEvalPointDenyReasonStable(t *testing.T) {
	p := regMw(t, "g3", func(cx *Registrar) error {
		WrapGuard(cx, ToolCall, "first", func(ctx context.Context, req ToolCallReq) (Verdict, string) {
			return Deny, "第一条"
		})
		WrapGuard(cx, ToolCall, "second", func(ctx context.Context, req ToolCallReq) (Verdict, string) {
			return Deny, "第二条"
		})
		return nil
	})
	asm, err := Assemble(Spec{Plugins: entries(p)})
	if err != nil {
		t.Fatal(err)
	}
	// 跑多次，原因必须稳定（并行不影响顺序）。
	for i := 0; i < 20; i++ {
		v, why := EvalPoint(asm, PointToolCallMw, context.Background(), ToolCallReq{})
		if v != Deny || why != "第一条" {
			t.Fatalf("第 %d 次：原因应稳定为「第一条」，实际 %q", i, why)
		}
	}
}

// 只有 Allow 没有 Deny → Allow。
func TestEvalPointAllow(t *testing.T) {
	p := regMw(t, "g4", func(cx *Registrar) error {
		WrapGuard(cx, ToolCall, "yes", func(ctx context.Context, req ToolCallReq) (Verdict, string) {
			return Allow, ""
		})
		return nil
	})
	asm, err := Assemble(Spec{Plugins: entries(p)})
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := EvalPoint(asm, PointToolCallMw, context.Background(), ToolCallReq{}); v != Allow {
		t.Fatalf("应 Allow，实际 %v", v)
	}
}

// 拒绝原因空时用规则名。
func TestEvalPointDenyReasonFallsBackToName(t *testing.T) {
	p := regMw(t, "g5", func(cx *Registrar) error {
		WrapGuard(cx, ToolCall, "myrule", func(ctx context.Context, req ToolCallReq) (Verdict, string) {
			return Deny, ""
		})
		return nil
	})
	asm, err := Assemble(Spec{Plugins: entries(p)})
	if err != nil {
		t.Fatal(err)
	}
	if _, why := EvalPoint(asm, PointToolCallMw, context.Background(), ToolCallReq{}); why != "myrule" {
		t.Fatalf("原因应回退到规则名，实际 %q", why)
	}
}

// 判定能读到 Req 内容（按工具名判定）。
func TestEvalPointSeesRequest(t *testing.T) {
	p := regMw(t, "g6", func(cx *Registrar) error {
		WrapGuard(cx, ToolCall, "danger", func(ctx context.Context, req ToolCallReq) (Verdict, string) {
			if req.Tool == "rm" {
				return Deny, "不许 rm"
			}
			return Allow, ""
		})
		return nil
	})
	asm, err := Assemble(Spec{Plugins: entries(p)})
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := EvalPoint(asm, PointToolCallMw, context.Background(), ToolCallReq{Tool: "rm"}); v != Deny {
		t.Fatal("rm 应被拒")
	}
	if v, _ := EvalPoint(asm, PointToolCallMw, context.Background(), ToolCallReq{Tool: "ls"}); v != Allow {
		t.Fatal("ls 应放行")
	}
}

// 没登记判定时 Abstain，且 HasGuards 为 false。
func TestEvalPointEmpty(t *testing.T) {
	asm, err := Assemble(Spec{})
	if err != nil {
		t.Fatal(err)
	}
	if asm.HasGuards(PointToolCallMw) {
		t.Fatal("没登记时不该有判定")
	}
	if v, _ := EvalPoint(asm, PointToolCallMw, context.Background(), ToolCallReq{}); v != Abstain {
		t.Fatalf("空判定区应 Abstain，实际 %v", v)
	}
}

// L11（2026-11-13）：点位与 Req 类型写错时，判定退化为 Abstain，不拿零值 req 猜结论。
//
// 这不是编译错误（点位只给名字，Req 由 fn 的类型单独定），所以只能运行时兜。
// 修复前：断言丢 ok，fn 拿到零值 req 照常执行，结论照样生效——一条写错的判定
// 可能因此放行或拒绝，比不判更危险。
func TestEvalPointTypeMismatchAbstains(t *testing.T) {
	p := regMw(t, "g8", func(cx *Registrar) error {
		// 故意写错：点位是 ToolCall，Req 却用 DeliverReq。
		WrapGuard(cx, ToolCall, "mismatch", func(ctx context.Context, req DeliverReq) (Verdict, string) {
			return Deny, "这条不该生效"
		})
		return nil
	})
	asm, err := Assemble(Spec{Plugins: entries(p)})
	if err != nil {
		t.Fatal(err)
	}
	v, why := EvalPoint(asm, PointToolCallMw, context.Background(), ToolCallReq{Tool: "shell"})
	if v != Abstain {
		t.Fatalf("类型不符应退化为 Abstain，实际 %v %q", v, why)
	}
}

// GuardNames 按登记顺序列出。
func TestGuardNames(t *testing.T) {
	p := regMw(t, "g7", func(cx *Registrar) error {
		WrapGuard(cx, ToolCall, "a", func(ctx context.Context, req ToolCallReq) (Verdict, string) { return Abstain, "" })
		WrapGuard(cx, ToolCall, "b", func(ctx context.Context, req ToolCallReq) (Verdict, string) { return Abstain, "" })
		return nil
	})
	asm, err := Assemble(Spec{Plugins: entries(p)})
	if err != nil {
		t.Fatal(err)
	}
	names := asm.GuardNames(PointToolCallMw)
	if len(names) != 2 || names[0] != "a" || names[1] != "b" {
		t.Fatalf("判定顺序 = %v", names)
	}
}

// Authorizer 只能登记一个。
type fakeAuth struct{ deny bool }

func (f fakeAuth) Decide(ctx context.Context, req ToolCallReq) (Verdict, string, error) {
	if f.deny {
		return Deny, "被 Authorizer 拒", nil
	}
	return Allow, "", nil
}

func TestAuthorizerSingle(t *testing.T) {
	p1 := regMw(t, "auth1", func(cx *Registrar) error {
		return cx.SetAuthorizer(fakeAuth{deny: true})
	})
	asm, err := Assemble(Spec{Plugins: entries(p1)})
	if err != nil {
		t.Fatal(err)
	}
	if asm.Authorizer() == nil {
		t.Fatal("应装到 Authorizer")
	}
	v, why, err := asm.Authorizer().Decide(context.Background(), ToolCallReq{Tool: "x"})
	if err != nil || v != Deny || why != "被 Authorizer 拒" {
		t.Fatalf("Decide = %v %q %v", v, why, err)
	}
}

// 第二个组件登记 Authorizer → 装配失败。
func TestAuthorizerDuplicateRejected(t *testing.T) {
	p1 := regMw(t, "authA", func(cx *Registrar) error {
		return cx.SetAuthorizer(fakeAuth{})
	})
	p2 := regMw(t, "authB", func(cx *Registrar) error {
		return cx.SetAuthorizer(fakeAuth{})
	})
	if _, err := Assemble(Spec{Plugins: entries(p1, p2)}); err == nil {
		t.Fatal("重复登记 Authorizer 应装配失败")
	}
}
