package fs

// 插件登记（thin-kernel-plan §2.2）：read_file / grep / glob / edit / write_file / list_dir。

import (
	"ybase/runtime/assemble"
	"ybase/runtime/component"
	"ybase/runtime/toolhub"
)

type plug struct{}

func init() { component.Register("fs", func() component.Component { return &plug{} }) }

func (*plug) Meta() component.Meta {
	return component.Meta{Name: "fs", Version: "0.1.0", Default: true, Effects: []string{component.EffectTools}, Requires: []string{assemble.ServiceWorkdir}}
}

func (*plug) Setup(cx *component.Registrar) error {
	root, err := component.Use[string](cx, assemble.ServiceWorkdir)
	if err != nil {
		return err
	}
	for _, it := range Items(root) {
		cx.Register(assemble.PointPermItems, it.ID, it)
	}
	// 工具装配期构造一次，各会话共用同一批只读对象（框架设计 04 4.2）。工作目录来自 service:workdir，
	// 与会话无关；将来若按会话给不同根，从 toolhub.Cx.Ctx 取。
	tools := []*toolhub.Tool{
		ReadFile(root).WithItems(ItemRead),
		Grep(root).WithItems(ItemRead),
		Glob(root).WithItems(ItemRead),
		Edit(root).WithItems(ItemWrite),
		WriteFile(root).WithItems(ItemWrite),
		ListDir(root).WithItems(ItemRead),
	}
	cx.Register(assemble.PointSessionTools, "fs", assemble.ToolsEntry{Order: 10,
		Tools: func() []*toolhub.Tool { return tools }})
	return nil
}

func (*plug) Start(*component.Scope) error { return nil }
