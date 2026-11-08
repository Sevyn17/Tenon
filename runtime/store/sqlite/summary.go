package sqlite

import (
	"strings"

	"github.com/Sevyn17/tenon/runtime/session"
)

// summaryMax：条目一行预览的上限（rune）。
const summaryMax = 200

// Summarize 生成条目的一行预览（长会话列表 / 折叠视图只读它）。宿主侧逻辑，内核不解释内容。
func Summarize(e *session.Entry) string {
	it := e.Item
	switch it.Kind {
	case session.KindUser:
		return clip(text(it.User.Content))
	case session.KindAssistant:
		s := text(it.Assistant.Content)
		for _, c := range it.Assistant.ToolCalls {
			s += " ⚙" + c.Name
		}
		return clip(s)
	case session.KindToolResult:
		return clip("[" + it.ToolResult.Status.State + "] " + text(it.ToolResult.Output))
	case session.KindCompaction:
		return clip("[压缩] " + text(it.Compaction.Summary))
	default:
		return "[" + it.Kind + "]"
	}
}

func text(c session.Content) string {
	var b strings.Builder
	for _, p := range c {
		if p.Kind == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

func clip(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > summaryMax {
		return string(r[:summaryMax]) + "…"
	}
	return s
}
