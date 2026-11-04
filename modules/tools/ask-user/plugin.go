package askuser

// 插件登记：ask-user（标准模块，缺省开）。ask_user 工具经 service:ui 向人提问。

import (
	"context"

	"ybase/runtime/assemble"
	"ybase/runtime/component"
	"ybase/runtime/toolhub"
)

type plug struct{}

func init() { component.Register("ask-user", func() component.Component { return &plug{} }) }

func (*plug) Meta() component.Meta {
	return component.Meta{Name: "ask-user", Version: "0.1.0", Default: true, Effects: []string{component.EffectTools}, Requires: []string{assemble.ServiceUI}}
}

func (*plug) Setup(cx *component.Registrar) error {
	ui, err := component.Use[assemble.UI](cx, assemble.ServiceUI)
	if err != nil {
		return err
	}
	ask := AskUserIn(func(ctx context.Context, sid, q string, o []string) (string, error) {
		return assemble.AskIn(ui, ctx, sid, q, o) // 带会话 id（审查 P1-28）
	})
	cx.Register(assemble.PointSessionTools, "ask-user", assemble.ToolsEntry{Order: 21,
		Tools: func() []*toolhub.Tool { return []*toolhub.Tool{ask} }})
	return nil
}

func (*plug) Start(*component.Scope) error { return nil }
