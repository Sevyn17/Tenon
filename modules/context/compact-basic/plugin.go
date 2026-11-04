package compactbasic

// 插件登记：compact-basic（标准模块）。提供 service:compactor（*Compactor，实现 agent.Compactor 与 runtime.Redactable），
// 开位 before_compact（乙类）：压缩时读点位上的登记，应用不必再接线（审查 P1-25：原先靠应用调 Wire，SDK 没调）。

import (
	"ybase/runtime/assemble"
	"ybase/runtime/component"
)

// PointBeforeCompact 登记 BeforeCompact，返回错误即拒绝本次压缩。
const PointBeforeCompact = "before_compact"

// Config compact-basic 配置。
type Config struct {
	Auto      bool    `cfg:"auto" default:"true" title:"自动压缩" widget:"switch"`
	Headroom  int     `cfg:"headroom" default:"16384" title:"阈值余量（token）"`
	KeepRatio float64 `cfg:"keep_ratio" default:"0.16" title:"保留最近比例" widget:"slider"`
	Model     string  `cfg:"model" title:"摘要模型（空 = 会话模型）"`
}

// SetRedact 装脱敏函数（runtime.Redactable）。
func (c *Compactor) SetRedact(f func(string) string) { c.Redact = f }

type compactPlugin struct{}

func init() {
	component.Register("compact-basic", func() component.Component { return compactPlugin{} })
}

func (compactPlugin) Meta() component.Meta {
	return component.Meta{Name: "compact-basic", Version: "0.1.0", Default: true, Effects: []string{component.EffectRewriteResult}, Config: &Config{},
		Requires: []string{assemble.ServiceModelhub}, Provides: []string{assemble.ServiceCompactor}}
}

func (compactPlugin) Setup(cx *component.Registrar) error {
	cfg := cx.Config().(*Config)
	m, err := component.Use[*assemble.Models](cx, assemble.ServiceModelhub)
	if err != nil {
		return err
	}
	alias := cfg.Model
	if alias == "" {
		alias = m.Model
	}
	c := &Compactor{Auto: cfg.Auto, Headroom: cfg.Headroom, KeepRatio: cfg.KeepRatio,
		Model: alias, Summarize: HubSummarizer(m.Hub, alias)}
	// 窗口取会话模型计划内最小窗口（plugin-manager §5.2）；摘要模型窗口更小时取更小者。
	cat := m.Hub.Catalog()
	for _, a := range []string{m.Model, alias} {
		p, err := cat.Resolve(a)
		if err != nil {
			continue
		}
		if w := cat.MinWindow(p); w > 0 && (c.Window == 0 || w < c.Window) {
			c.Window = w
			if md, _, err := cat.Lookup(p[0].Model); err == nil {
				c.MaxOutput = md.MaxOutput
			}
		}
	}
	items := cx.PointItems(PointBeforeCompact)
	c.befores = func() []BeforeCompact {
		var out []BeforeCompact
		for _, it := range items() {
			if b, ok := it.Impl.(BeforeCompact); ok {
				out = append(out, b)
			}
		}
		return out
	}
	return cx.Provide("compactor", c)
}

func (compactPlugin) Start(*component.Scope) error { return nil }
