package subagent

// 插件登记（thin-kernel-plan §2.2）：subagent 委派工具，只给顶层会话（子会话不带 → 深度 1）。
// 子会话经 service:host 的 Child 构造（可空 = New）。

import (
	"fmt"

	"ybase-kernel/core/agent"
	"ybase-kernel/core/host"
	"ybase-kernel/core/plugin"
	kr "ybase-kernel/core/runtime"
	"ybase-kernel/core/session"
	"ybase-kernel/core/toolhub"
)

type plug struct{}

func init() { plugin.Register("subagent", func() plugin.Plugin { return &plug{} }) }

func (*plug) Meta() plugin.Meta {
	return plugin.Meta{Name: "subagent", Version: "0.1.0", Effects: []string{plugin.EffectTools}, Requires: []string{kr.ServiceHost}}
}

// ChildFactory 返回按 h.Child（可空 = h.New）构造子会话的工厂；调用时才取值，装配后由应用填好也可。
func ChildFactory(h *host.Host) host.Factory {
	return func(id string) (*agent.Agent, *session.Config, error) {
		f := h.Child
		if f == nil {
			f = h.New
		}
		if f == nil {
			return nil, nil, fmt.Errorf("subagent: 宿主未配置会话构造")
		}
		return f(id)
	}
}

func (*plug) Setup(cx *plugin.Registrar) error {
	h, err := plugin.Use[*host.Host](cx, kr.ServiceHost)
	if err != nil {
		return err
	}
	t := Tool(h, ChildFactory(h)).WithItems(ItemSpawn)
	for _, it := range Items() {
		cx.Register(kr.PointPermItems, it.ID, it)
	}
	// 子会话不再带委派工具（深度上限 1）。这里用 Visible 而不是重造工具。
	cx.Register(kr.PointSessionTools, "subagent", kr.ToolsEntry{Order: 60,
		Tools:   func() []*toolhub.Tool { return []*toolhub.Tool{t} },
		Visible: func(sc *kr.SessionCx) bool { return !sc.Child }})
	return nil
}

func (*plug) Start(*plugin.Scope) error { return nil }
