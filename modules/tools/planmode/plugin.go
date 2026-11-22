package planmode

// 插件登记：plan-mode（标准模块，缺省开）。exit_plan 经 service:ui 的结构化交互弹计划卡，经 service:host 退出 Plan 模式。
// 界面不支持结构化交互（终端）时不登记工具。exit_plan 只在 Plan 模式下显露（agent 构造请求时按 work_mode 收窄）。

import (
	"context"
	"encoding/json"

	"github.com/Sevyn17/tenon/runtime/assemble"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/host"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// Name 插件名。
const Name = "plan-mode"

type plug struct{}

func init() { component.Register(Name, func() component.Component { return &plug{} }) }

func (*plug) Meta() component.Meta {
	return component.Meta{Name: Name, Version: "0.1.0", Description: "Plan mode: investigate and submit a plan, execute after approval",
		Default: true, Effects: []string{component.EffectTools},
		Requires: []string{assemble.ServiceUI, "?" + assemble.ServiceHost}}
}

func (*plug) Setup(cx *component.Registrar) error {
	ui, err := component.Use[assemble.UI](cx, assemble.ServiceUI)
	if err != nil {
		return err
	}
	h, ok, err := component.UseOptional[*host.Host](cx, assemble.ServiceHost)
	if err != nil {
		return err
	}
	if _, inter := ui.(assemble.Interactor); !inter || !ok || h == nil {
		return nil
	}
	tool := ExitPlan(func(ctx context.Context, sid, title string, body map[string]any, schema json.RawMessage) (json.RawMessage, error) {
		ans, err := assemble.Interact(ui, ctx, sid, assemble.Interaction{Kind: Kind, Title: title, Body: body, Schema: schema})
		return ans.Value, err
	}, HostSession{H: h})
	component.RegisterAt(cx, assemble.SessionTools, Name, assemble.ToolsEntry{Order: 22,
		Tools: func() []*toolhub.Tool { return []*toolhub.Tool{tool} }})
	return nil
}

func (*plug) Start(*component.Scope) error { return nil }
