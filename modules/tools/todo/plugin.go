package todo

// 插件登记（thin-kernel-plan §2.2）：todo_write（计划变化经 service:ui 显示）（可选，缺省关）。

import (
	"strings"

	"github.com/Sevyn17/tenon/runtime/assemble"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

type plug struct{}

func init() { component.Register("todo", func() component.Component { return &plug{} }) }

func (*plug) Meta() component.Meta {
	return component.Meta{Name: "todo", Version: "0.1.0", Effects: []string{component.EffectTools}, Requires: []string{assemble.ServiceUI}}
}

func (*plug) Setup(cx *component.Registrar) error {
	ui, err := component.Use[assemble.UI](cx, assemble.ServiceUI)
	if err != nil {
		return err
	}
	marks := map[string]string{"pending": "☐", "in_progress": "▶", "completed": "☑"}
	todo := TodoWriteIn(func(sid string, ts []Todo) {
		var b strings.Builder
		b.WriteString("\n📋 计划\n")
		for _, t := range ts {
			b.WriteString("  " + marks[t.Status] + " " + t.Content + "\n")
		}
		assemble.PrintIn(ui, sid, b.String()) // 带会话 id（审查 P1-28）
	})
	component.RegisterAt(cx, assemble.SessionTools, "todo", assemble.ToolsEntry{Order: 20,
		Tools: func() []*toolhub.Tool { return []*toolhub.Tool{todo} }})
	return nil
}

func (*plug) Start(*component.Scope) error { return nil }
