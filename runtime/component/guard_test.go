package component

import (
	"testing"

	"tenon/runtime/mail"
)

// TestRegistrarGuard：组件通过 Registrar.Guard 登记规则，装配后按装配顺序可取回，
// 规则名带组件前缀（04 4.2、08 D4）。
func TestRegistrarGuard(t *testing.T) {
	f := &fake{setup: func(cx *Registrar) error {
		cx.Guard(GuardRule{Name: cx.Plugin() + "/block", On: GuardDeliver, Check: func(req GuardRequest) (Verdict, string) {
			if req.Envelope == nil || req.Envelope.To.Kind != mail.AddrSession {
				t.Fatalf("Deliver 点位应带信封：%+v", req)
			}
			return Deny, "组件不允许"
		}})
		return nil
	}}
	name := reg(t, "guard-test", f)
	asm, err := Assemble(Spec{Plugins: entries(name)})
	if err != nil {
		t.Fatal(err)
	}
	rules := asm.GuardRules()
	if len(rules) != 1 {
		t.Fatalf("应登记 1 条规则：%+v", rules)
	}
	r := rules[0]
	if r.Name != name+"/block" || r.On != GuardDeliver || r.Check == nil {
		t.Fatalf("规则记录：%+v", r)
	}
	v, why := r.Check(GuardRequest{Point: GuardDeliver, Envelope: &mail.Envelope{To: mail.Address{Kind: mail.AddrSession, ID: "s1"}}})
	if v != Deny || why != "组件不允许" {
		t.Fatalf("规则结论：%v %q", v, why)
	}
	// 空名字用组件名。
	empty := &fake{setup: func(cx *Registrar) error {
		cx.Guard(GuardRule{On: GuardTurn, Check: func(GuardRequest) (Verdict, string) { return Abstain, "" }})
		return nil
	}}
	name2 := reg(t, "guard-test2", empty)
	asm2, err := Assemble(Spec{Plugins: entries(name2)})
	if err != nil {
		t.Fatal(err)
	}
	if got := asm2.GuardRules(); len(got) != 1 || got[0].Name != name2 {
		t.Fatalf("空名字应取组件名：%+v", got)
	}
}
