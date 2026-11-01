package anthropic

// anthropic-messages 适配器测试（M3.11）：解析、缓存断点与会话缓存亲和 id（D5）。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ybase-kernel/core/modelhub"
	"ybase-kernel/core/session"
)

var _ = json.Marshal
var _ = io.EOF
var _ = http.StatusOK
var _ = session.NewTextPart
var _ = context.Background

func serve(t *testing.T, status int, hdr map[string]string, body string, seen *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, seen)
		}
		for k, v := range hdr {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
}

// collect 收全部事件，断言恰好一个终止事件且在最后。
func collectOne(t *testing.T, ch <-chan modelhub.Event) (text string, term modelhub.Event) {
	t.Helper()
	var sb strings.Builder
	terms := 0
	for ev := range ch {
		if ev.Done != nil || ev.Failed != nil {
			terms++
			term = ev
			continue
		}
		if terms > 0 {
			t.Fatal("终止事件之后还有事件")
		}
		sb.WriteString(ev.TextDelta)
	}
	if terms != 1 {
		t.Fatalf("终止事件数 = %d，应为 1", terms)
	}
	return sb.String(), term
}

func baseReq() *modelhub.Request {
	return &modelhub.Request{RequestID: "r1", SessionKey: "sess-key", Req: session.Request{
		System:   []session.SystemBlock{{Text: "sys", Breakpoint: true}},
		Messages: []session.Message{{Role: "user", Content: session.Content{session.NewTextPart("hi")}}},
		Tools:    []session.ToolSpec{{Name: "echo", Schema: json.RawMessage(`{"type":"object"}`)}},
	}}
}

func TestAnthropicParseCacheAndBreakpoint(t *testing.T) {
	ev := func(name, data string) string { return "event: " + name + "\ndata: " + data + "\n\n" }
	sse := ev("message_start", `{"type":"message_start","message":{"usage":{"input_tokens":10,"cache_read_input_tokens":90,"cache_creation_input_tokens":5}}}`) +
		ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`) +
		ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"想"}}`) +
		ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-1"}}`) +
		ev("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`) +
		ev("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"好"}}`) +
		ev("content_block_start", `{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"tu_1","name":"echo"}}`) +
		ev("content_block_delta", `{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"s\":2}"}}`) +
		ev("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":4}}`) +
		ev("message_stop", `{"type":"message_stop"}`)
	var seen map[string]any
	srv := serve(t, 200, nil, sse, &seen)
	defer srv.Close()
	text, term := collectOne(t, Anthropic{}.Stream(context.Background(), baseReq(), modelhub.Endpoint{BaseURL: srv.URL, Model: "claude"}))
	if text != "好" || term.Done == nil {
		t.Fatalf("text=%q term=%+v", text, term)
	}
	r := term.Done.Reply
	if r.Usage.CacheRead != 90 || r.Usage.CacheWrite != 5 || r.Usage.Output != 4 {
		t.Fatalf("缓存用量: %+v", r.Usage)
	}
	if len(r.ToolCalls) != 1 || r.ToolCalls[0].WireID != "tu_1" || string(r.ToolCalls[0].Args) != `{"s":2}` {
		t.Fatalf("工具调用: %+v", r.ToolCalls)
	}
	if len(r.Reasoning) != 1 || r.Reasoning[0].Opaque == nil {
		t.Fatalf("签名思考应原样保留: %+v", r.Reasoning)
	}
	sys, _ := json.Marshal(seen["system"])
	if !strings.Contains(string(sys), "cache_control") {
		t.Fatalf("缓存断点未映射为 cache_control: %s", sys)
	}

	// 回放：同协议、带签名的思考随 assistant 消息回传。
	req := baseReq()
	req.Req.Messages = append(req.Req.Messages, session.Message{Role: "assistant", Reasoning: r.Reasoning,
		Content: r.Content})
	body := anthropicBody(req, modelhub.Endpoint{Model: "claude"})
	b, _ := json.Marshal(body["messages"])
	if !strings.Contains(string(b), `"signature":"sig-1"`) {
		t.Fatalf("签名思考未回传: %s", b)
	}

	// DeepSeek 的 Anthropic 端点不给签名：思考块仍要回传，且 thinking 字段即使为空也必须出现。
	empty := ""
	req = baseReq()
	req.Req.Messages = append(req.Req.Messages, session.Message{Role: "assistant",
		Reasoning: []session.ReasoningBlock{{Text: &empty, Opaque: anthropicOpaque{Protocol: "anthropic-messages"}}},
		ToolCalls: []session.ToolCall{{ID: "c1", Name: "read_file", Args: json.RawMessage(`{}`)}}})
	b, _ = json.Marshal(anthropicBody(req, modelhub.Endpoint{Model: "deepseek-flash"})["messages"])
	if !strings.Contains(string(b), `"type":"thinking","thinking":""`) {
		t.Fatalf("无签名思考未回传: %s", b)
	}
}
