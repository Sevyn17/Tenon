package component

import (
	"testing"
)

// 组件登记使用配置原型：装配结束后能按组件名查到字段表。
func TestRegistrarAgentConfig(t *testing.T) {
	type memoryUse struct {
		Store string `toml:"store" required:"true"`
		Top   int    `toml:"top" default:"5" tenant:"true" limit:"min"`
	}
	p := reg(t, "memory", &fake{setup: func(cx *Registrar) error {
		return cx.AgentConfig(&memoryUse{})
	}})
	other := reg(t, "plain", &fake{})
	asm, err := Assemble(Spec{Plugins: entries(p, other)})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	schemas := asm.AgentSchemas()
	if len(schemas) != 1 || schemas[0].Component != p {
		t.Fatalf("AgentSchemas = %+v", schemas)
	}
	if keys := schemas[0].Keys(); len(keys) != 2 || keys[0] != "store" {
		t.Fatalf("键：%v", keys)
	}
	sc, ok := asm.AgentSchema(p)
	if !ok || sc.Component != p {
		t.Fatalf("AgentSchema = %+v %v", sc, ok)
	}
	if _, ok := asm.AgentSchema(other); ok {
		t.Fatal("没登记使用配置的组件不该查到")
	}
}

// 重复登记同一份使用配置算装配错误。
func TestRegistrarAgentConfigDuplicate(t *testing.T) {
	type dupUse struct {
		A int `toml:"a"`
	}
	p := reg(t, "dup", &fake{setup: func(cx *Registrar) error {
		if err := cx.AgentConfig(&dupUse{}); err != nil {
			return err
		}
		return cx.AgentConfig(&dupUse{})
	}})
	if _, err := Assemble(Spec{Plugins: entries(p)}); err == nil {
		t.Fatal("重复登记应当报错")
	}
}
