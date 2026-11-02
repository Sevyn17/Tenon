package codemode

// 插件登记（thin-kernel-plan §2.2）：run_code。子调用回到同一会话 hub 的调用栈。

import (
	"ybase-kernel/core/plugin"
	kr "ybase-kernel/core/runtime"
	"ybase-kernel/core/toolhub"
)

type plug struct{}

func init() { plugin.Register("code-mode", func() plugin.Plugin { return &plug{} }) }

func (*plug) Meta() plugin.Meta {
	return plugin.Meta{Name: "code-mode", Version: "0.1.0", Effects: []string{plugin.EffectTools, plugin.EffectExec}}
}

func (*plug) Setup(cx *plugin.Registrar) error {
	for _, it := range Items() {
		cx.Register(kr.PointPermItems, "code-mode", it)
	}
	// 子调用回到本次调用所在的 hub（toolhub.Cx.Hub），工具本身与会话无关。
	cx.Register(kr.PointSessionTools, "code-mode", kr.ToolsEntry{Order: 90,
		Tools: func() []*toolhub.Tool { return []*toolhub.Tool{Tool()} }})
	return nil
}

func (*plug) Start(*plugin.Scope) error { return nil }
