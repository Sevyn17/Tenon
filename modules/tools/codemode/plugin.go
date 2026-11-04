package codemode

// 插件登记（thin-kernel-plan §2.2）：run_code。子调用回到同一会话 hub 的调用栈。

import (
	"ybase/runtime/assemble"
	"ybase/runtime/component"
	"ybase/runtime/toolhub"
)

type plug struct{}

func init() { component.Register("code-mode", func() component.Component { return &plug{} }) }

func (*plug) Meta() component.Meta {
	return component.Meta{Name: "code-mode", Version: "0.1.0", Effects: []string{component.EffectTools, component.EffectExec}}
}

func (*plug) Setup(cx *component.Registrar) error {
	for _, it := range Items() {
		cx.Register(assemble.PointPermItems, "code-mode", it)
	}
	// 子调用回到本次调用所在的 hub（toolhub.Cx.Hub），工具本身与会话无关。
	cx.Register(assemble.PointSessionTools, "code-mode", assemble.ToolsEntry{Order: 90,
		Tools: func() []*toolhub.Tool { return []*toolhub.Tool{Tool()} }})
	return nil
}

func (*plug) Start(*component.Scope) error { return nil }
