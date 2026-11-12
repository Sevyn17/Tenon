package modelhub

// 协议适配器共用的纯文本辅助。HTTP 相关的（PostSSE、HTTPFailure、RunStream）
// 在 modules/internal/httpx：内核禁令不许本包引用 net/http（18 方案第十一节）。

import (
	"fmt"
	"strings"

	"github.com/Sevyn17/tenon/runtime/session"
)

// ContentString 把内容拼成文本。非文本片段（图片等）换成占位说明，不静默丢弃：
// 模型至少知道这里有东西没发过去（2026-11-04 审查 P2 工具体验）。
func ContentString(c session.Content) string {
	var sb strings.Builder
	for _, p := range c {
		switch {
		case p.Kind == "text":
			sb.WriteString(p.Text)
		case p.Kind != "":
			mime := ""
			if p.Media != nil {
				mime = p.Media.MIME
			}
			fmt.Fprintf(&sb, "[%s %s 未发送：当前协议适配器只发文本]", p.Kind, mime)
		}
	}
	return sb.String()
}
