package askuser

// 插件登记：ask-user（标准模块，缺省开）。ask_user 工具经 service:ui 向人提问；界面支持结构化交互时另给 ask_choice。

import (
	"context"
	"encoding/json"

	"tenon/runtime/assemble"
	"tenon/runtime/component"
	"tenon/runtime/toolhub"
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
	tools := []*toolhub.Tool{ask}
	if _, ok := ui.(assemble.Interactor); ok { // 界面能显示结构化交互（桌面、网页服务）时才给 ask_choice
		tools = append(tools, AskChoice(func(ctx context.Context, sid, kind, title string, body map[string]any) (json.RawMessage, error) {
			ans, err := assemble.Interact(ui, ctx, sid, assemble.Interaction{Kind: kind, Title: title, Body: body})
			return ans.Value, err
		}))
	}
	component.RegisterAt(cx, assemble.SessionTools, "ask-user", assemble.ToolsEntry{Order: 21,
		Tools: func() []*toolhub.Tool { return tools }})
	return nil
}

func (*plug) Start(*component.Scope) error { return nil }
