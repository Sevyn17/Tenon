package skills

// 插件登记（thin-kernel-plan §2.2）：skill 工具 + 技能目录系统块。会话创建时扫描一次，目录进系统块后不变。

import (
	"ybase-kernel/core/pkgs"
	"ybase-kernel/core/plugin"
	kr "ybase-kernel/core/runtime"
	"ybase-kernel/core/session"
	"ybase-kernel/core/toolhub"
)

const valueKey = "skills"

type plug struct{}

func init() { plugin.Register("skills", func() plugin.Plugin { return &plug{} }) }

func (*plug) Meta() plugin.Meta {
	return plugin.Meta{Name: "skills", Version: "0.1.0", Effects: []string{plugin.EffectTools, plugin.EffectSystemPrompt}, Requires: []string{kr.ServiceWorkdir, kr.ServiceUI, "?" + pkgs.Service}}
}

func (*plug) Setup(cx *plugin.Registrar) error {
	root, err := plugin.Use[string](cx, kr.ServiceWorkdir)
	if err != nil {
		return err
	}
	ui, err := plugin.Use[kr.UI](cx, kr.ServiceUI)
	if err != nil {
		return err
	}
	// 插件包的技能目录排在内置根之后（同名技能项目 / 用户级优先）。
	ps, err := pkgs.From(cx)
	if err != nil {
		return err
	}
	var extra []string
	for _, k := range ps {
		if k.SkillsDir != "" {
			extra = append(extra, k.SkillsDir)
		}
	}
	for _, it := range Items() {
		cx.Register(kr.PointPermItems, "skills", it)
	}
	// 技能索引装配期扫一次，各会话共用（工具与系统块读同一份）。
	s := NewSkills(root, extra...)
	for _, w := range s.Warnings {
		ui.Warn("技能", w)
	}
	sk := s.Tool().WithItems(ItemLoad)
	cx.Register(kr.PointSessionTools, "skills", kr.ToolsEntry{Order: 25,
		Tools: func() []*toolhub.Tool { return []*toolhub.Tool{sk} }})
	cx.Register(kr.PointSystemBlock, "skills", kr.BlockEntry{Order: 20,
		Func: func(*kr.SessionCx) (*session.SystemBlock, error) { return s.Block(), nil }})
	return nil
}

func (*plug) Start(*plugin.Scope) error { return nil }
