package compactbasic

// 插件登记：compact-basic（标准模块）。提供 service:compactor（*Compactor，实现 agent.Compactor 与 runtime.Redactable），
// 开位 before_compact（乙类）：压缩时读点位上的登记，应用不必再接线（审查 P1-25：原先靠应用调 Wire，SDK 没调）。

import (
	"github.com/Sevyn17/tenon/runtime/assemble"
	"github.com/Sevyn17/tenon/runtime/component"
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

// compactPlugin 每次装配一个新实例：Setup 记下压缩器与模型服务，Start 时缺省模型若已变（models 填了
// slot:model_sources 时缺省模型到 Start 才定），重新绑定摘要模型与窗口。
type compactPlugin struct {
	c      *Compactor
	m      *assemble.Models
	model  string // 配置的摘要模型；"" = 跟会话缺省模型
	seenAt string // Setup 时看到的缺省模型
}

func init() {
	component.Register("compact-basic", func() component.Component { return &compactPlugin{} })
}

func (*compactPlugin) Meta() component.Meta {
	return component.Meta{Name: "compact-basic", Version: "0.1.0", Description: "上下文压缩：对话太长时把早期内容压成摘要", Default: true, Effects: []string{component.EffectRewriteResult}, Config: &Config{},
		Requires: []string{assemble.ServiceModelhub}, Provides: []string{assemble.ServiceCompactor}}
}

func (p *compactPlugin) Setup(cx *component.Registrar) error {
	cfg := cx.Config().(*Config)
	m, err := component.Use[*assemble.Models](cx, assemble.ServiceModelhub)
	if err != nil {
		return err
	}
	c := &Compactor{Auto: cfg.Auto, Headroom: cfg.Headroom, KeepRatio: cfg.KeepRatio}
	p.c, p.m, p.model, p.seenAt = c, m, cfg.Model, m.Model
	bind(c, m, cfg.Model)
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

// Start 缺省模型在 Setup 之后才定（models 的 Start 先于本组件）时，按定下来的模型重新绑定。
// 没变时什么也不做（框架自己的装配都是这样，行为与以前一样）。
func (p *compactPlugin) Start(*component.Scope) error {
	if p.c != nil && p.m != nil && p.m.Model != p.seenAt {
		p.c.Window, p.c.MaxOutput = 0, 0
		bind(p.c, p.m, p.model)
		p.seenAt = p.m.Model
	}
	return nil
}

// bind 摘要模型（配置的，空 = 会话缺省模型）与窗口：窗口取会话模型计划内最小窗口（plugin-manager §5.2）；
// 摘要模型窗口更小时取更小者。
func bind(c *Compactor, m *assemble.Models, model string) {
	alias := model
	if alias == "" {
		alias = m.Model
	}
	c.Model, c.Summarize = alias, HubSummarizer(m.Hub, alias)
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
}
