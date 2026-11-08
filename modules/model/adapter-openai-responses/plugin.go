package responses

// 插件登记：adapter-openai-responses（标准模块，缺省开）。在 Setup 里向 service:modelhub 的 Hub 登记 openai-responses 协议适配器。

import (
	"github.com/Sevyn17/tenon/runtime/assemble"
	"github.com/Sevyn17/tenon/runtime/component"
)

type adapterPlugin struct{}

func init() {
	component.Register("adapter-openai-responses", func() component.Component { return adapterPlugin{} })
}

func (adapterPlugin) Meta() component.Meta {
	return component.Meta{Name: "adapter-openai-responses", Version: "0.1.0", Default: true, Effects: []string{component.EffectNetwork},
		Requires: []string{assemble.ServiceModelhub}}
}

func (adapterPlugin) Setup(cx *component.Registrar) error {
	m, err := component.Use[*assemble.Models](cx, assemble.ServiceModelhub)
	if err != nil {
		return err
	}
	return m.Hub.RegisterAdapter(OpenAIResponses{})
}

func (adapterPlugin) Start(*component.Scope) error { return nil }
