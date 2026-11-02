package todo

// 插件登记（thin-kernel-plan §2.2）：todo_write（计划变化经 service:ui 显示）（可选，缺省关）。

import (
	"strings"

	"ybase-kernel/core/plugin"
	kr "ybase-kernel/core/runtime"
	"ybase-kernel/core/toolhub"
)

type plug struct{}

func init() { plugin.Register("todo", func() plugin.Plugin { return &plug{} }) }

func (*plug) Meta() plugin.Meta {
	return plugin.Meta{Name: "todo", Version: "0.1.0", Effects: []string{plugin.EffectTools}, Requires: []string{kr.ServiceUI}}
}

func (*plug) Setup(cx *plugin.Registrar) error {
	ui, err := plugin.Use[kr.UI](cx, kr.ServiceUI)
	if err != nil {
		return err
	}
	marks := map[string]string{"pending": "☐", "in_progress": "▶", "completed": "☑"}
	todo := TodoWrite(func(ts []Todo) {
		var b strings.Builder
		b.WriteString("\n📋 计划\n")
		for _, t := range ts {
			b.WriteString("  " + marks[t.Status] + " " + t.Content + "\n")
		}
		ui.Print(b.String())
	})
	cx.Register(kr.PointSessionTools, "todo", kr.ToolsEntry{Order: 20,
		Tools: func() []*toolhub.Tool { return []*toolhub.Tool{todo} }})
	return nil
}

func (*plug) Start(*plugin.Scope) error { return nil }
