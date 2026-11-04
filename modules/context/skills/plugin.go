package skills

// 插件登记（thin-kernel-plan §2.2）：skill 工具 + 技能目录系统块。会话创建时扫描一次，目录进系统块后不变。

import (
	"ybase/runtime/assemble"
	"ybase/runtime/component"
	"ybase/runtime/pkgs"
	"ybase/runtime/session"
	"ybase/runtime/toolhub"
)

const valueKey = "skills"

type plug struct{}

func init() { component.Register("skills", func() component.Component { return &plug{} }) }

func (*plug) Meta() component.Meta {
	return component.Meta{Name: "skills", Version: "0.1.0", Effects: []string{component.EffectTools, component.EffectSystemPrompt}, Requires: []string{assemble.ServiceWorkdir, assemble.ServiceUI, "?" + assemble.ServiceTrust, "?" + pkgs.Service}}
}

func (*plug) Setup(cx *component.Registrar) error {
	root, err := component.Use[string](cx, assemble.ServiceWorkdir)
	if err != nil {
		return err
	}
	ui, err := component.Use[assemble.UI](cx, assemble.ServiceUI)
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
		cx.Register(assemble.PointPermItems, "skills", it)
	}
	// 技能索引装配期扫一次，各会话共用（工具与系统块读同一份）。
	s := NewSkillsTrust(root, assemble.ProjectTrusted(cx), extra...) // 不受信项目不读项目技能（审查 P1-4）
	for _, w := range s.Warnings {
		ui.Warn("技能", w)
	}
	sk := s.Tool().WithItems(ItemLoad)
	cx.Register(assemble.PointSessionTools, "skills", assemble.ToolsEntry{Order: 25,
		Tools: func() []*toolhub.Tool { return []*toolhub.Tool{sk} }})
	cx.Register(assemble.PointSystemBlock, "skills", assemble.BlockEntry{Order: 20,
		Func: func(*assemble.SessionCx) (*session.SystemBlock, error) { return s.Block(), nil }})
	return nil
}

func (*plug) Start(*component.Scope) error { return nil }
