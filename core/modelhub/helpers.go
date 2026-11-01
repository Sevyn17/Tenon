package modelhub

// 协议适配器共用的辅助函数（适配器在 plugins/model/adapter-*）。

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ybase-kernel/core/session"
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

func ContentString(c session.Content) string {
	var sb strings.Builder
	for _, p := range c {
		if p.Kind == "text" {
			sb.WriteString(p.Text)
		}
	}
	return sb.String()
}
