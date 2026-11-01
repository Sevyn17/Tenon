package responses

// 插件登记：adapter-openai-responses（核心，缺省开）。在 Setup 里向 service:modelhub 的 Hub 登记 openai-responses 协议适配器。

import (
	"ybase-kernel/core/plugin"
	kr "ybase-kernel/core/runtime"
)

type adapterPlugin struct{}

func init() {
	plugin.Register("adapter-openai-responses", func() plugin.Plugin { return adapterPlugin{} })
}

func (adapterPlugin) Meta() plugin.Meta {
	return plugin.Meta{Name: "adapter-openai-responses", Version: "0.1.0", Default: true, Effects: []string{plugin.EffectNetwork},
		Requires: []string{kr.ServiceModelhub}}
}

func (adapterPlugin) Setup(cx *plugin.SetupCx) error {
	m, err := plugin.Use[*kr.Models](cx, kr.ServiceModelhub)
	if err != nil {
		return err
	}
	return m.Hub.RegisterAdapter(OpenAIResponses{})
}

func (adapterPlugin) Start(*plugin.Scope) error { return nil }
