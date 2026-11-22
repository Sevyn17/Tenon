package subagent

// 插件登记（thin-kernel-plan §2.2）：subagent 委派工具，只给顶层会话（子会话不带 → 深度 1）。
// 子会话经 service:host 的 Child 构造（可空 = New）。

import (
	"github.com/Sevyn17/tenon/runtime/agentdef"
	"github.com/Sevyn17/tenon/runtime/assemble"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/host"
	"github.com/Sevyn17/tenon/runtime/tasks"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// Definition 子智能体会话的内置定义名（09 9.3、计划/01 T1b-2c）：模板归平台命名空间，
// 子会话由宿主按这个名字构造，不再按会话 id 猜角色。
const Definition = "subagent"

// templateManifest 子智能体的内置定义模板。行为不变：不带 tools / use / prompt，
// 工具集合仍由 Host 的会话构造按 Child 决定（09 9.3「行为不变」）。
const templateManifest = `format = "tenon/v1"
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

// PluginConfig 插件配置 [use.subagent] 的参数开关（Mortise docs/62 K5、docs/56 第 2 节）：
// 每个按次参数一个开关，开着的才出现在委派工具 schema 里。task 必有，不可关。
//
//	[[plugin]]
//	name = "subagent"
//	[plugin.config]
//	param_model = true
type PluginConfig struct {
	SystemPrompt bool `cfg:"param_system_prompt" default:"true" title:"开放 system_prompt" widget:"switch" description:"委派时可追加子智能体的系统提示（接在基础提示词之后）"`
	Model        bool `cfg:"param_model" default:"false" title:"开放 model" widget:"switch" description:"委派时可指定子智能体的模型；关着时跟随主会话"`
	Mode         bool `cfg:"param_mode" default:"true" title:"开放 mode" widget:"switch" description:"委派时可指定子智能体的权限档位；只能收窄，最多与主会话一样宽"`
	Tools        bool `cfg:"param_tools" default:"true" title:"开放 tools" widget:"switch" description:"委派时可给子智能体工具白名单；只能是主会话有的工具"`
}

// Params 配置对应的参数开关。
func (c *PluginConfig) Params() Params {
	if c == nil {
		return Params{SystemPrompt: true, Mode: true, Tools: true}
	}
	return Params{SystemPrompt: c.SystemPrompt, Model: c.Model, Mode: c.Mode, Tools: c.Tools}
}

type plug struct{}

func init() { component.Register("subagent", func() component.Component { return &plug{} }) }

func (*plug) Meta() component.Meta {
	return component.Meta{Name: "subagent", Version: "0.1.0", Description: "Delegate a task to a subagent in its own child session", Effects: []string{component.EffectTools}, Requires: []string{assemble.ServiceHost, tasks.Key.Maybe()}, Config: &PluginConfig{}}
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
	// 任务登记（Mortise docs/62 K2）：产品提供 service:tasks 时报委派，没有就不报。
	sink, _, err := component.GetOptional(cx, tasks.Key)
	if err != nil {
		return err
	}
	cfg, _ := cx.Config().(*PluginConfig)
	t := ToolWithParams(h, h.ChildFactory(), sink, cfg.Params()).WithItems(ItemSpawn)
	for _, it := range Items() {
		component.RegisterAt(cx, assemble.PermItems, it.ID, it)
	}
	// 子会话不再带委派工具（深度上限 1）。这里用 Visible 而不是重造工具。
	component.RegisterAt(cx, assemble.SessionTools, "subagent", assemble.ToolsEntry{Order: 60,
		Tools:   func() []*toolhub.Tool { return []*toolhub.Tool{t} },
		Visible: func(sc *assemble.SessionCx) bool { return !sc.Child }})
	return nil
}

func (*plug) Start(*component.Scope) error { return nil }
