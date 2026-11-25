package host

import (
	"strings"
	"unicode"
)

// TaskTitleRunes 派出会话标题的最大字符数（超出加省略号）。
const TaskTitleRunes = 60

// TaskTitle 派出会话（子会话、独立会话）的标题：任务的第一个非空行，压掉多余空白，截到 TaskTitleRunes 个字符。
// 界面据此显示会话名，不再只有短 id。取不到时返回空串（界面退回短 id）。
func TaskTitle(task string) string {
	for _, line := range strings.Split(task, "\n") {
		t := strings.Join(strings.FieldsFunc(line, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
		if t == "" {
			continue
		}
		if r := []rune(t); len(r) > TaskTitleRunes {
			return strings.TrimSpace(string(r[:TaskTitleRunes])) + "…"
		}
		return t
	}
	return ""
}
