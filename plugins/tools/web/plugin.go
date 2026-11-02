package web

// 插件登记（thin-kernel-plan §2.2）：web_fetch / web_search（DeepSeek 搜索）。

import (
	"ybase-kernel/core/plugin"
	kr "ybase-kernel/core/runtime"
	"ybase-kernel/core/toolhub"
)

type plug struct{}

func init() { plugin.Register("web", func() plugin.Plugin { return &plug{} }) }

func (*plug) Meta() plugin.Meta {
	return plugin.Meta{Name: "web", Version: "0.1.0", Effects: []string{plugin.EffectTools, plugin.EffectNetwork}}
}

func (*plug) Setup(cx *plugin.Registrar) error {
	w := &Web{Search: NewDeepSeekSearch()}
	for _, it := range Items() {
		cx.Register(kr.PointPermItems, it.ID, it)
	}
	cx.Register(kr.PointSessionTools, "web", kr.ToolsEntry{Order: 40,
		Func: func(*kr.SessionCx) ([]*toolhub.Tool, error) {
			ts := w.Tools()
			ts[0].WithItems(ItemFetch)
			ts[1].WithItems(ItemSearch)
			return ts, nil
		}})
	return nil
}

func (*plug) Start(*plugin.Scope) error { return nil }
