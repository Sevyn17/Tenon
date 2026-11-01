package redact

// 插件登记（thin-kernel-plan §2.2）：工具结果脱敏 Transform 层（最外层），并给压缩器装上脱敏（可选依赖 service:compactor）。

import (
	"ybase-kernel/core/plugin"
	kr "ybase-kernel/core/runtime"
	"ybase-kernel/core/toolhub"
)

type plug struct{}

func init() { plugin.Register("redact", func() plugin.Plugin { return &plug{} }) }

func (*plug) Meta() plugin.Meta {
	return plugin.Meta{Name: "redact", Version: "0.1.0", Effects: []string{plugin.EffectRewriteResult}, Requires: []string{"?service:compactor"}}
}

func (*plug) Setup(cx *plugin.SetupCx) error {
	red := Default() // 进程环境中名字像密钥的变量值 + 常见密钥模式
	c, ok, err := plugin.UseOptional[kr.Redactable](cx, kr.ServiceCompactor)
	if err != nil {
		return err
	}
	if ok && c != nil {
		c.SetRedact(func(s string) string { t, _ := red.String(s); return t })
	}
	cx.Register(kr.PointSessionLayer, "redact", kr.LayerEntry{Order: 0,
		Func: func(*kr.SessionCx) ([]toolhub.Layer, error) { return []toolhub.Layer{Layer(red)}, nil }})
	cx.Register(kr.PointSessionLayer, "redact-guard", kr.LayerEntry{Guard: true, Order: 1,
		Func: func(*kr.SessionCx) ([]toolhub.Layer, error) { return []toolhub.Layer{WriteGuard()}, nil }})
	return nil
}

func (*plug) Start(*plugin.Scope) error { return nil }
