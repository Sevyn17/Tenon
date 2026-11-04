package anthropic

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"ybase/runtime/modelhub"
	"ybase/runtime/session"
)

// redacted_thinking 块原样保留并在下一次请求里回传（2026-11-04 审查 P1-31：原先整块丢掉，
// 工具回合里的思考块不完整）。
func TestRedactedThinkingRoundTrip(t *testing.T) {
	ev := func(name, data string) string { return "event: " + name + "\ndata: " + data + "\n\n" }
	sse := ev("message_start", `{"type":"message_start","message":{"usage":{"input_tokens":1}}}`) +
		ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"redacted_thinking","data":"ENCRYPTED-BLOB"}}`) +
		ev("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tu_1","name":"echo"}}`) +
		ev("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{}"}}`) +
		ev("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":1}}`) +
		ev("message_stop", `{"type":"message_stop"}`)
	srv := serve(t, 200, nil, sse, nil)
	defer srv.Close()
	_, term := collectOne(t, Anthropic{}.Stream(context.Background(), baseReq(), modelhub.Endpoint{BaseURL: srv.URL, Model: "claude"}))
	if term.Done == nil {
		t.Fatalf("应成功：%+v", term.Failed)
	}
	reply := term.Done.Reply
	if len(reply.Reasoning) != 1 {
		t.Fatalf("redacted_thinking 应保留为一个推理块：%+v", reply.Reasoning)
	}
	// 下一次请求：assistant 回复 + 工具结果。
	reply.ToolCalls[0].ID = "c1"
	req := baseReq()
	req.Req.Messages = append(req.Req.Messages,
		session.Message{Role: "assistant", Reasoning: reply.Reasoning, ToolCalls: reply.ToolCalls},
		session.Message{Role: "tool", ToolCallID: "c1", Content: session.Content{session.NewTextPart("ok")}})
	b, _ := json.Marshal(anthropicBody(req, modelhub.Endpoint{Model: "claude"}))
	if !strings.Contains(string(b), `"type":"redacted_thinking"`) || !strings.Contains(string(b), `"data":"ENCRYPTED-BLOB"`) {
		t.Fatalf("回放请求应带回 redacted_thinking：%s", b)
	}
}
