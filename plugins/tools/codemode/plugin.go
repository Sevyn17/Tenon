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
	cx.Register(kr.PointSessionTools, "code-mode", kr.ToolsEntry{Order: 90,
		Func: func(sc *kr.SessionCx) ([]*toolhub.Tool, error) { return []*toolhub.Tool{Tool(sc.Hub)}, nil }})
	return nil
}

func (*plug) Start(*plugin.Scope) error { return nil }
