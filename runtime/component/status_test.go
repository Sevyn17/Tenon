package component

import (
	"testing"

	"github.com/Sevyn17/tenon/runtime/callctx"
	"github.com/Sevyn17/tenon/runtime/inject"
)

func TestRegistrarStatus(t *testing.T) {
	fn := func(*callctx.Ctx) []inject.Item {
		return []inject.Item{{Body: "任务 1"}}
	}
	name := reg(t, "todo", &fake{setup: func(cx *Registrar) error {
		return cx.Status("todo*", fn)
	}})
	asm, err := Assemble(Spec{Plugins: entries(name)})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	regs := asm.Statuses()
	if len(regs) != 1 {
		t.Fatalf("Statuses=%+v", regs)
	}
	if regs[0].Key != "todo*" || regs[0].Owner != name || regs[0].Fn == nil {
		t.Fatalf("登记信息不对：%+v", regs[0])
	}
	items := inject.Collect(nil, regs)
	if len(items) != 1 || items[0].Key != "todo" || items[0].Source != name || items[0].Kind != inject.KindStatus {
		t.Fatalf("装配后的注入片段不对：%+v", items)
	}
}

func TestRegistrarStatusRejectsEmptyKey(t *testing.T) {
	name := reg(t, "bad", &fake{setup: func(cx *Registrar) error {
		return cx.Status("", func(*callctx.Ctx) []inject.Item { return nil })
	}})
	if _, err := Assemble(Spec{Plugins: entries(name)}); err == nil {
		t.Fatal("空 key 应让装配失败")
	}
}

func TestRegistrarStatusRejectsNilFunc(t *testing.T) {
	name := reg(t, "nil-fn", &fake{setup: func(cx *Registrar) error {
		return cx.Status("x", nil)
	}})
	if _, err := Assemble(Spec{Plugins: entries(name)}); err == nil {
		t.Fatal("空登记函数应让装配失败")
	}
}
