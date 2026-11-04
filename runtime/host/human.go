package host

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"ybase/runtime/mail"
)

// deliverHuman 投递给人（11 11.2）。v1 没有端点注册表：发布 human.delivered 事件，
// 界面按 corr 回答（回信信封 KindReply + corr 投回会话，或走既有 Confirm 命令）。
func (h *Host) deliverHuman(ctx context.Context, env mail.Envelope) error {
	h.recordTrace(env.Trace, "", "envelope", fmt.Sprintf("%s -> %s（%s/%s）", env.From, env.To, env.Kind, env.Mode))
	h.publish("", "human.delivered", map[string]any{
		"from": env.From.String(), "to": env.To.String(), "corr": env.Corr,
		"body": env.Body, "trace": env.Trace,
	})
	return nil
}

// resolveHuman 把人工确认的回答（回信信封）交给等待中的工具确认。返回是否有等待者。
func (h *Host) resolveHuman(corr, body string) bool {
	h.humanMu.Lock()
	ch, ok := h.humanWait[corr]
	h.humanMu.Unlock()
	if !ok {
		return false
	}
	select {
	case ch <- decisionFromBody(body):
	default:
	}
	return true
}

// decisionFromBody 解析确认回答：JSON `{"approve":true}` / `{"decision":"approve"}`，
// 或 yes / approve / allow / 批准 / 同意 等文本；其余按拒绝处理。
func decisionFromBody(body string) bool {
	s := strings.TrimSpace(body)
	if strings.HasPrefix(s, "{") {
		var m map[string]any
		if json.Unmarshal([]byte(s), &m) == nil {
			if v, ok := m["approve"].(bool); ok {
				return v
			}
			if v, ok := m["allow"].(bool); ok {
				return v
			}
			if v, ok := m["decision"].(string); ok {
				return allowWord(v)
			}
		}
	}
	return allowWord(s)
}

func allowWord(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "y", "yes", "ok", "true", "1", "allow", "approve", "approved", "批准", "同意", "是", "允许":
		return true
	}
	return false
}
