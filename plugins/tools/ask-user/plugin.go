package askuser

// 插件登记：ask-user（核心，缺省开）。ask_user 工具经 service:ui 向人提问。

import (
	"ybase-kernel/core/plugin"
	kr "ybase-kernel/core/runtime"
	"ybase-kernel/core/toolhub"
)

type plug struct{}

func init() { plugin.Register("ask-user", func() plugin.Plugin { return &plug{} }) }

func (*plug) Meta() plugin.Meta {
	return plugin.Meta{Name: "ask-user", Version: "0.1.0", Default: true, Effects: []string{plugin.EffectTools}, Requires: []string{kr.ServiceUI}}
}

func (*plug) Setup(cx *plugin.Registrar) error {
	ui, err := plugin.Use[kr.UI](cx, kr.ServiceUI)
	if err != nil {
		return err
	}
	ask := AskUser(ui.Ask)
	cx.Register(kr.PointSessionTools, "ask-user", kr.ToolsEntry{Order: 21,
		Tools: func() []*toolhub.Tool { return []*toolhub.Tool{ask} }})
	return nil
}

func (*plug) Start(*plugin.Scope) error { return nil }
