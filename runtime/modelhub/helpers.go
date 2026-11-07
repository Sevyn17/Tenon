package modelhub

// 协议适配器共用的辅助函数（适配器在 modules/model/adapter-*）。

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"tenon/runtime/session"
)

// HTTPFailure 按状态码与错误体归一。
func HTTPFailure(code int, h http.Header, body []byte) *Failure {
	msg := strings.TrimSpace(string(body))
	f := &Failure{Message: fmt.Sprintf("http %d: %s", code, msg)}
	low := strings.ToLower(msg)
	switch {
	case code == 401 || code == 403:
		f.Code = CodeAuth
	case code == 429:
		f.Code = CodeRateLimit
		if strings.Contains(low, "quota") || strings.Contains(low, "insufficient") {
			f.Code = CodeQuota
		}
	case code == 402:
		f.Code = CodeQuota
	case code == 400 || code == 404 || code == 413 || code == 422:
		f.Code = CodeInvalidRequest
		if strings.Contains(low, "context") && (strings.Contains(low, "length") || strings.Contains(low, "window") || strings.Contains(low, "maximum")) {
			f.Code = CodeContextOverflow
		}
	case code == 408:
		f.Code = CodeTimeout
	case code >= 500:
		f.Code = CodeServer
	default:
		f.Code = CodeProtocol
	}
	if ra := h.Get("Retry-After"); ra != "" {
		if s, err := strconv.Atoi(ra); err == nil {
			f.RetryAfter = time.Duration(s) * time.Second
		} else if t, err := http.ParseTime(ra); err == nil {
			f.RetryAfter = time.Until(t)
		}
	}
	return f
}

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
