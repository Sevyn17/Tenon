package component

// 判定区（04 4.3.2）：WrapGuard 登记、EvalPoint 求值。
//
// 2026-11-12：原 cx.Guard(GuardRule{...}) 删除（生产零登记，整套空转），本文件从测 GuardRule
// 改成测 WrapGuard + EvalPoint（同一件事的新机制）。

import (
	"context"
	"testing"

	"github.com/Sevyn17/tenon/runtime/mail"
)

// WrapGuard 登记的判定能被 EvalPoint 取到并求值。
func TestWrapGuardDeliver(t *testing.T) {
	name := t.Name() + "/guard"
	p := reg(t, "guard-test", &fake{setup: func(cx *Registrar) error {
		WrapGuard(cx, Deliver, name+"/block",
			func(ctx context.Context, req GuardRequest) (Verdict, string) {
				if req.Envelope == nil || req.Envelope.To.Kind != mail.AddrSession {
					t.Errorf("Deliver 点位应带信封：%+v", req)
				}
				return Deny, "组件不允许"
			})
		return nil
	}})
	asm, err := Assemble(Spec{Plugins: entries(p)})
	if err != nil {
		t.Fatal(err)
	}
	if !asm.HasGuards(PointDeliverMw) {
		t.Fatal("判定应已登记")
	}
	names := asm.GuardNames(PointDeliverMw)
	if len(names) != 1 || names[0] != name+"/block" {
		t.Fatalf("判定名：%v", names)
	}
	v, why := EvalPoint(asm, PointDeliverMw, context.Background(),
		GuardRequest{Point: GuardDeliver, Envelope: &mail.Envelope{To: mail.Address{Kind: mail.AddrSession, ID: "s1"}}})
	if v != Deny || why != "组件不允许" {
		t.Fatalf("应 Deny：%v %q", v, why)
	}
}

// Abstain 不影响其他判定；任一 Deny 即拒。
func TestWrapGuardAnyDeny(t *testing.T) {
	name := t.Name() + "/multi"
	p := reg(t, "guard-multi", &fake{setup: func(cx *Registrar) error {
		WrapGuard(cx, Deliver, name+"/abstain",
			func(ctx context.Context, req GuardRequest) (Verdict, string) { return Abstain, "" })
		WrapGuard(cx, Deliver, name+"/allow",
			func(ctx context.Context, req GuardRequest) (Verdict, string) { return Allow, "" })
		WrapGuard(cx, Deliver, name+"/deny",
			func(ctx context.Context, req GuardRequest) (Verdict, string) { return Deny, "拒绝" })
		return nil
	}})
	asm, err := Assemble(Spec{Plugins: entries(p)})
	if err != nil {
		t.Fatal(err)
	}
	v, why := EvalPoint(asm, PointDeliverMw, context.Background(), GuardRequest{Point: GuardDeliver})
	if v != Deny || why != "拒绝" {
		t.Fatalf("任一 Deny 即拒：%v %q", v, why)
	}
}
