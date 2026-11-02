package fs

// 插件登记（thin-kernel-plan §2.2）：read_file / grep / glob / edit / write_file / list_dir。

import (
	"ybase-kernel/core/plugin"
	kr "ybase-kernel/core/runtime"
	"ybase-kernel/core/toolhub"
)

type plug struct{}

func init() { plugin.Register("fs", func() plugin.Plugin { return &plug{} }) }

func (*plug) Meta() plugin.Meta {
	return plugin.Meta{Name: "fs", Version: "0.1.0", Default: true, Effects: []string{plugin.EffectTools}, Requires: []string{kr.ServiceWorkdir}}
}

func (*plug) Setup(cx *plugin.Registrar) error {
	root, err := plugin.Use[string](cx, kr.ServiceWorkdir)
	if err != nil {
		return err
	}
	for _, it := range Items(root) {
		cx.Register(kr.PointPermItems, it.ID, it)
	}
	cx.Register(kr.PointSessionTools, "fs", kr.ToolsEntry{Order: 10,
		Func: func(*kr.SessionCx) ([]*toolhub.Tool, error) {
			return []*toolhub.Tool{
				ReadFile(root).WithItems(ItemRead),
				Grep(root).WithItems(ItemRead),
				Glob(root).WithItems(ItemRead),
				Edit(root).WithItems(ItemWrite),
				WriteFile(root).WithItems(ItemWrite),
				ListDir(root).WithItems(ItemRead),
			}, nil
		}})
	return nil
}

func (*plug) Start(*plugin.Scope) error { return nil }
