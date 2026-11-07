package component

import (
	"testing"

	"tenon/runtime/ext"
)

type sbx interface{ Mode() string }
type sbxImpl struct{}

func (sbxImpl) Mode() string { return "ws" }

// 类型化键（2026-11-04 审查 P1-48）：Requires 用 key.Need()/Maybe()，Get 返回键的类型；
// 提供者缺席的可选依赖返回 false，不报错。
func TestTypedKeys(t *testing.T) {
	key := ext.NewKey[sbx]("service:sbx")
	absent := ext.NewKey[sbx]("service:absent")
	var got sbx
	var has bool
	n := reg(t, "user", &fake{meta: Meta{Requires: []string{key.Need(), absent.Maybe()}}, setup: func(cx *Registrar) error {
		var err error
		if got, err = Get(cx, key); err != nil {
			return err
		}
		_, has, err = GetOptional(cx, absent)
		return err
	}})
	if _, err := Assemble(Spec{Plugins: entries(n), Inject: map[string]any{key.Name(): sbx(sbxImpl{})}}); err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Mode() != "ws" || has {
		t.Fatalf("got=%v has=%v", got, has)
	}
	if absent.Maybe() != "?service:absent" {
		t.Fatal(absent.Maybe())
	}
}
