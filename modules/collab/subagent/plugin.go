package subagent

// 插件登记（thin-kernel-plan §2.2）：subagent 委派工具，只给顶层会话（子会话不带 → 深度 1）。
// 子会话经 service:host 的 Child 构造（可空 = New）。

import (
	"ybase/runtime/agentdef"
	"ybase/runtime/assemble"
	"ybase/runtime/component"
	"ybase/runtime/host"
	"ybase/runtime/toolhub"
)

// Definition 子智能体会话的内置定义名（09 9.3、计划/01 T1b-2c）：模板归平台命名空间，
// 子会话由宿主按这个名字构造，不再按会话 id 猜角色。
const Definition = "subagent"

// templateManifest 子智能体的内置定义模板。行为不变：不带 tools / use / prompt，
// 工具集合仍由 Host 的会话构造按 Child 决定（09 9.3「行为不变」）。
const templateManifest = `format = "ybase/v1"
id = "subagent"
description = "子智能体：由父会话的 subagent 工具委派一个自包含子任务（深度 1）。"
`

// TemplateSource 子智能体内置定义模板的来源（平台层）；Setup 里交给 Registrar.Template。
func TemplateSource() (agentdef.Source, error) {
	m := agentdef.Mem()
	if err := m.Put(agentdef.Ref{Namespace: agentdef.Platform, Name: Definition},
		agentdef.Files{agentdef.ManifestName: []byte(templateManifest)}); err != nil {
		return nil, err
	}
	return m, nil
}

type plug struct{}

func init() { component.Register("subagent", func() component.Component { return &plug{} }) }

func (*plug) Meta() component.Meta {
	return component.Meta{Name: "subagent", Version: "0.1.0", Effects: []string{component.EffectTools}, Requires: []string{assemble.ServiceHost}}
}

func (*plug) Setup(cx *component.Registrar) error {
	h, err := component.Use[*host.Host](cx, assemble.ServiceHost)
	if err != nil {
		return err
	}
	src, err := TemplateSource()
	if err != nil {
		return err
	}
	cx.Template(src)
	if err := cx.AgentConfig(&UseConfig{}); err != nil {
		return err
	}
	t := Tool(h, h.ChildFactory()).WithItems(ItemSpawn)
	for _, it := range Items() {
		cx.Register(assemble.PointPermItems, it.ID, it)
	}
	// 子会话不再带委派工具（深度上限 1）。这里用 Visible 而不是重造工具。
	cx.Register(assemble.PointSessionTools, "subagent", assemble.ToolsEntry{Order: 60,
		Tools:   func() []*toolhub.Tool { return []*toolhub.Tool{t} },
		Visible: func(sc *assemble.SessionCx) bool { return !sc.Child }})
	return nil
}

func (*plug) Start(*component.Scope) error { return nil }
