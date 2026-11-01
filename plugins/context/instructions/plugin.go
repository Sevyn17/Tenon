package instructions

// 插件登记（thin-kernel-plan §2.2）：AGENTS.md 系统块 + 嵌套指令 Transform 层（每会话一份，已装载集合属于本会话）。

import (
	"ybase-kernel/core/plugin"
	kr "ybase-kernel/core/runtime"
	"ybase-kernel/core/session"
	"ybase-kernel/core/toolhub"
)

const valueKey = "instructions"

type plug struct{}

func init() { plugin.Register("instructions", func() plugin.Plugin { return &plug{} }) }

func (*plug) Meta() plugin.Meta {
	return plugin.Meta{Name: "instructions", Version: "0.1.0", Default: true, Effects: []string{plugin.EffectSystemPrompt, plugin.EffectRewriteResult}, Requires: []string{kr.ServiceWorkdir}}
}

func get(sc *kr.SessionCx, root string) *Instructions {
	if in, ok := sc.Values[valueKey].(*Instructions); ok {
		return in
	}
	in := NewInstructions(root)
	sc.Values[valueKey] = in
	return in
}

func (*plug) Setup(cx *plugin.SetupCx) error {
	root, err := plugin.Use[string](cx, kr.ServiceWorkdir)
	if err != nil {
		return err
	}
	cx.Register(kr.PointSessionLayer, "instructions", kr.LayerEntry{Order: 20,
		Func: func(sc *kr.SessionCx) ([]toolhub.Layer, error) { return []toolhub.Layer{get(sc, root).Layer()}, nil }})
	cx.Register(kr.PointSystemBlock, "instructions", kr.BlockEntry{Order: 10,
		Func: func(sc *kr.SessionCx) (*session.SystemBlock, error) { return get(sc, root).Block() }})
	return nil
}

func (*plug) Start(*plugin.Scope) error { return nil }
