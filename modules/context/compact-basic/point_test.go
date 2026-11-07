package compactbasic

import (
	"context"
	"errors"
	"testing"

	"tenon/runtime/assemble"
	"tenon/runtime/component"
	"tenon/runtime/modelhub"
)

// guardComp 另一个组件：在 before_compact 点位登记一条拒绝。
type guardComp struct{}

func (guardComp) Meta() component.Meta { return component.Meta{Name: "zz-guard", Default: true} }
func (guardComp) Setup(cx *component.Registrar) error {
	cx.Register(PointBeforeCompact, "deny", BeforeCompact(func(context.Context, string, int) error {
		return errors.New("先抽取记忆")
	}))
	return nil
}
func (guardComp) Start(*component.Scope) error { return nil }

// 别的组件登记的 before_compact 在装配后就生效，不需要应用调用接线函数
// （2026-11-04 审查 P1-25：原先靠应用调 Wire，SDK 没调，登记全部被忽略）。
func TestBeforeCompactPointWithoutWire(t *testing.T) {
	hub := modelhub.New(modelhub.NewCatalog(), func(string) (string, error) { return "", nil }, nil)
	asm, err := component.Assemble(component.Spec{
		Plugins:   []component.Entry{{Name: "compact-basic"}, {Name: "zz-guard"}},
		Instances: map[string]component.Component{"compact-basic": compactPlugin{}, "zz-guard": guardComp{}},
		Inject:    map[string]any{assemble.ServiceModelhub: &assemble.Models{Hub: hub, Model: "m"}},
		Defaults:  assemble.SlotDefaults(),
	})
	if err != nil {
		t.Fatal(err)
	}
	v, ok := asm.Value(assemble.ServiceCompactor)
	if !ok {
		t.Fatal("没有压缩器")
	}
	c := v.(*Compactor)
	if n := len(c.allBefore()); n != 1 {
		t.Fatalf("应读到 1 条 before_compact 登记，得 %d", n)
	}
}
