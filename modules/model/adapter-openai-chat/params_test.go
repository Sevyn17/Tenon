package openaichat

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"tenon/runtime/modelhub"
	"tenon/runtime/session"
)

// ToolChoice 与 Reasoning 映射到请求体；两个推理字段都发时只取一个（2026-11-04 审查 P1-32）。
func TestToolChoiceReasoningAndSingleField(t *testing.T) {
	effort := "high"
	r := &modelhub.Request{Req: session.Request{
		Messages: []session.Message{{Role: "user", Content: session.Content{session.NewTextPart("hi")}}},
		Tools:    []session.ToolSpec{{Name: "echo", Schema: json.RawMessage(`{"type":"object"}`)}},
		Params:   session.Params{ToolChoice: &session.ToolChoice{Mode: "tool", Name: "echo"}, Reasoning: &effort},
	}}
	b, _ := json.Marshal(openAIBody(r, modelhub.Endpoint{Model: "m"}))
	s := string(b)
	if !strings.Contains(s, `"tool_choice":{"function":{"name":"echo"},"type":"function"}`) {
		t.Fatalf("tool_choice 未映射：%s", s)
	}
	if !strings.Contains(s, `"reasoning_effort":"high"`) {
		t.Fatalf("reasoning_effort 未映射：%s", s)
	}

	sse := `data: {"choices":[{"delta":{"reasoning_content":"想","reasoning":"想"}}]}` + "\n\n" +
		`data: {"choices":[{"delta":{"content":"好"},"finish_reason":"stop"}]}` + "\n\n" + "data: [DONE]\n\n"
	out := make(chan modelhub.Event, 16)
	term := parseOpenAI(context.Background(), strings.NewReader(sse), "m", "", out)
	if term.Done == nil || len(term.Done.Reply.Reasoning) != 1 || *term.Done.Reply.Reasoning[0].Text != "想" {
		t.Fatalf("两个推理字段不应拼两遍：%+v", term)
	}
}
