package redact

// 插件登记（thin-kernel-plan §2.2）：工具结果脱敏 Transform 层（最外层），并给压缩器装上脱敏（可选依赖 service:compactor）。

import (
	"github.com/Sevyn17/tenon/runtime/assemble"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

type plug struct{}

func init() { component.Register("redact", func() component.Component { return &plug{} }) }

func (*plug) Meta() component.Meta {
	return component.Meta{Name: "redact", Version: "0.1.0", Effects: []string{component.EffectRewriteResult}, Requires: []string{"?" + assemble.ServiceCompactor}}
}

func (*plug) Setup(cx *component.Registrar) error {
	red := Default() // 进程环境中名字像密钥的变量值 + 常见密钥模式
	c, ok, err := component.UseOptional[assemble.Redactable](cx, assemble.ServiceCompactor)
	if err != nil {
		return err
	}
	if ok && c != nil {
		c.SetRedact(func(s string) string { t, _ := red.String(s); return t })
	}
	component.RegisterAt(cx, assemble.SessionLayer, "redact", assemble.LayerEntry{Order: 0,
		Func: func(*assemble.SessionCx) ([]toolhub.Layer, error) { return []toolhub.Layer{Layer(red)}, nil }})
	component.RegisterAt(cx, assemble.SessionLayer, "redact-guard", assemble.LayerEntry{Guard: true, Order: 1,
		Func: func(*assemble.SessionCx) ([]toolhub.Layer, error) { return []toolhub.Layer{WriteGuard()}, nil }})
	return nil
}

func (*plug) Start(*component.Scope) error { return nil }
