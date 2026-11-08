package web

// 插件登记（thin-kernel-plan §2.2）：web_fetch / web_search（DeepSeek 搜索）。

import (
	"tenon/runtime/assemble"
	"tenon/runtime/component"
	"tenon/runtime/toolhub"
)

type plug struct{}

func init() { component.Register("web", func() component.Component { return &plug{} }) }

func (*plug) Meta() component.Meta {
	return component.Meta{Name: "web", Version: "0.1.0", Effects: []string{component.EffectTools, component.EffectNetwork}}
}

func (*plug) Setup(cx *component.Registrar) error {
	w := &Web{Search: NewDeepSeekSearch()}
	for _, it := range Items() {
		component.RegisterAt(cx, assemble.PermItems, it.ID, it)
	}
	// 工具装配期构造一次（WithItems 只做一次，不再按会话重复追加）。
	ts := w.Tools()
	ts[0].WithItems(ItemFetch)
	ts[1].WithItems(ItemSearch)
	component.RegisterAt(cx, assemble.SessionTools, "web", assemble.ToolsEntry{Order: 40,
		Tools: func() []*toolhub.Tool { return ts }})
	return nil
}

func (*plug) Start(*component.Scope) error { return nil }
