package fs

// 插件登记（thin-kernel-plan §2.2）：read_file / grep / glob / edit / write_file / list_dir。

import (
	"github.com/Sevyn17/tenon/runtime/assemble"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/toolhub"
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
		component.RegisterAt(cx, assemble.PermItems, it.ID, it)
	}
	// 工具装配期构造一次，各会话共用同一批只读对象（框架设计 04 4.2）。工作目录来自 service:workdir，
	// 与会话无关；将来若按会话给不同根，从 toolhub.Cx.Ctx 取。
	// 读后写检查（审查 P1-43）：read_file / edit / write_file 共用一份按会话的读写记录。
	tr := &Tracker{}
	tools := []*toolhub.Tool{
		ReadFile(root, tr).WithItems(ItemRead),
		Grep(root).WithItems(ItemRead),
		Glob(root).WithItems(ItemRead),
		Edit(root, tr).WithItems(ItemWrite),
		WriteFile(root, tr).WithItems(ItemWrite),
		ListDir(root).WithItems(ItemRead),
	}
	component.RegisterAt(cx, assemble.SessionTools, "fs", assemble.ToolsEntry{Order: 10,
		Tools: func() []*toolhub.Tool { return tools }})
	return nil
}

func (*plug) Start(*component.Scope) error { return nil }
