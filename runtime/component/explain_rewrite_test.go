package component

// H5 回归：03 - 约束清单第 19 行要求「开启后会改写模型所见内容的插件（如脱敏），
// 要在 explain 和启动横幅里明示」。
//
// 原来 Meta.Effects 的注释承诺「explain / 启动横幅明示」，但两个消费方都不存在
// （只有 appapi 把它给前端 UI）。这个用例锁住 explain 侧。

import (
	"strings"
	"testing"
)

// rewriteComp 一个声明「改写模型所见内容」的组件。
type rewriteComp struct{ name string }

func (c rewriteComp) Meta() Meta {
	return Meta{Name: c.name, Version: "0.1.0", Default: true,
		Effects: []string{EffectRewriteResult}}
}
func (c rewriteComp) Setup(*Registrar) error { return nil }
func (c rewriteComp) Start(*Scope) error     { return nil }

// 声明改写效果的组件在 explain 输出里带 ⚠ 标签。
func TestExplainMarksRewrite(t *testing.T) {
	name := t.Name() + "/rw"
	Register(name, func() Component { return rewriteComp{name: name} })
	asm, err := Assemble(Spec{Plugins: []Entry{{Name: name}}})
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	asm.Explain(&b, nil)
	out := b.String()
	if !strings.Contains(out, "改写模型所见内容") {
		t.Fatalf("explain 应明示改写类组件（H5）：\n%s", out)
	}
	if !strings.Contains(out, "⚠") {
		t.Fatalf("explain 应有 ⚠ 标记：\n%s", out)
	}
}

// 只声明 system_prompt 的也算改写（它改的是模型看到的提示词）。
func TestExplainMarksSystemPrompt(t *testing.T) {
	name := t.Name() + "/sp"
	Register(name, func() Component {
		return sysPromptComp{name: name}
	})
	asm, err := Assemble(Spec{Plugins: []Entry{{Name: name}}})
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	asm.Explain(&b, nil)
	if !strings.Contains(b.String(), "改系统提示词") {
		t.Fatalf("explain 应明示改系统提示词的组件：\n%s", b.String())
	}
}

type sysPromptComp struct{ name string }

func (c sysPromptComp) Meta() Meta {
	return Meta{Name: c.name, Version: "0.1.0", Default: true,
		Effects: []string{EffectSystemPrompt}}
}
func (c sysPromptComp) Setup(*Registrar) error { return nil }
func (c sysPromptComp) Start(*Scope) error     { return nil }

// 不声明改写效果的组件不带标签（不打扰）。
func TestExplainNoMarkForPlainComponent(t *testing.T) {
	name := t.Name() + "/plain"
	Register(name, func() Component { return plainComp{name: name} })
	asm, err := Assemble(Spec{Plugins: []Entry{{Name: name}}})
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	asm.Explain(&b, nil)
	if strings.Contains(b.String(), "⚠") {
		t.Fatalf("无改写效果的组件不该带 ⚠：\n%s", b.String())
	}
}

type plainComp struct{ name string }

func (c plainComp) Meta() Meta {
	return Meta{Name: c.name, Version: "0.1.0", Default: true,
		Effects: []string{EffectTools}}
}
func (c plainComp) Setup(*Registrar) error { return nil }
func (c plainComp) Start(*Scope) error     { return nil }
