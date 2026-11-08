package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/runtime/modelhub"
	"github.com/Sevyn17/tenon/runtime/session"
)

// ToolChoice 与 Reasoning 映射到请求体（2026-11-04 审查 P1-32：原先都没映射）。
func TestToolChoiceAndThinkingMapped(t *testing.T) {
	r := baseReq()
	effort := "medium"
	r.Req.Params.ToolChoice = &session.ToolChoice{Mode: "required"}
	r.Req.Params.Reasoning = &effort
	b, _ := json.Marshal(anthropicBody(r, modelhub.Endpoint{Model: "claude"}))
	s := string(b)
	if !strings.Contains(s, `"tool_choice":{"type":"any"}`) {
		t.Fatalf("tool_choice 未映射：%s", s)
	}
	if !strings.Contains(s, `"thinking":{"budget_tokens":4096,"type":"enabled"}`) {
		t.Fatalf("thinking 未映射：%s", s)
	}
}
