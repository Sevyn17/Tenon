package responses

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/runtime/modelhub"
	"github.com/Sevyn17/tenon/runtime/session"
)

// ToolChoice 映射到请求体（2026-11-04 审查 P1-32）。
func TestToolChoiceMapped(t *testing.T) {
	r := &modelhub.Request{Req: session.Request{
		Messages: []session.Message{{Role: "user", Content: session.Content{session.NewTextPart("hi")}}},
		Tools:    []session.ToolSpec{{Name: "echo", Schema: json.RawMessage(`{"type":"object"}`)}},
		Params:   session.Params{ToolChoice: &session.ToolChoice{Mode: "none"}},
	}}
	b, _ := json.Marshal(responsesBody(r, modelhub.Endpoint{Model: "m"}))
	if !strings.Contains(string(b), `"tool_choice":"none"`) {
		t.Fatalf("tool_choice 未映射：%s", b)
	}
}
