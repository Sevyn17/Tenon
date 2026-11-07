package responses

// openai-responses 适配器测试（M6.7）：解析文本 / 推理 / 工具调用、缓存用量、推理不透明回放、失败归一。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"tenon/runtime/modelhub"
	"tenon/runtime/session"
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

func sse(events ...string) string {
	var b strings.Builder
	for _, e := range events {
		b.WriteString("data: " + e + "\n\n")
	}
	return b.String()
}

func TestResponsesParse(t *testing.T) {
	body := sse(
		`{"type":"response.created","response":{"status":"in_progress"}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1"}}`,
		`{"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"想一想"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","encrypted_content":"ENC","summary":[{"type":"summary_text","text":"想一想"}]}}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"message","id":"m1"}}`,
		`{"type":"response.output_text.delta","output_index":1,"delta":"你"}`,
		`{"type":"response.output_text.delta","output_index":1,"delta":"好"}`,
		`{"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","call_id":"call_9","name":"read_file"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":2,"delta":"{\"path\":"}`,
		`{"type":"response.function_call_arguments.delta","output_index":2,"delta":"\"a.txt\"}"}`,
		`{"type":"response.output_item.done","output_index":2,"item":{"type":"function_call","call_id":"call_9","name":"read_file","arguments":"{\"path\":\"a.txt\"}"}}`,
		`{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":100,"output_tokens":20,"input_tokens_details":{"cached_tokens":64}}}}`,
	)
	var seen map[string]any
	srv := serve(t, 200, map[string]string{"Content-Type": "text/event-stream"}, body, &seen)
	defer srv.Close()
	sys := []session.SystemBlock{{Text: "你是助手"}}
	req := &modelhub.Request{Req: session.Request{System: sys, Messages: []session.Message{{Role: "user", Content: session.Content{session.NewTextPart("hi")}}},
		Tools: []session.ToolSpec{{Name: "read_file", Schema: json.RawMessage(`{"type":"object"}`)}}}, SessionKey: "s1"}
	text, term := collectOne(t, OpenAIResponses{}.Stream(context.Background(), req, modelhub.Endpoint{BaseURL: srv.URL, Model: "gpt-x", Compat: modelhub.Compat{PromptCacheKey: true}}))
	if text != "你好" {
		t.Fatalf("text %q", text)
	}
	if term.Done == nil {
		t.Fatalf("want done, got %+v", term.Failed)
	}
	r := term.Done.Reply
	if modelhub.ContentString(r.Content) != "你好" || len(r.ToolCalls) != 1 || r.ToolCalls[0].WireID != "call_9" || string(r.ToolCalls[0].Args) != `{"path":"a.txt"}` {
		t.Fatalf("reply %+v", r)
	}
	if term.Done.Stop.V != "tool_calls" {
		t.Fatalf("stop %q", term.Done.Stop.V)
	}
	if r.Usage.Input != 100 || r.Usage.CacheRead != 64 || r.Usage.Source != "Reported" {
		t.Fatalf("usage %+v", r.Usage)
	}
	if len(r.Reasoning) != 1 || *r.Reasoning[0].Text != "想一想" {
		t.Fatalf("reasoning %+v", r.Reasoning)
	}
	if seen["instructions"] != "你是助手" || seen["store"] != false || seen["prompt_cache_key"] != "s1" {
		t.Fatalf("body %+v", seen)
	}

	// 回放：推理块（同协议）原样带回，工具调用与结果按 call_id 配对。
	r.ToolCalls[0].ID = "k1"
	req2 := &modelhub.Request{Req: session.Request{Messages: []session.Message{
		{Role: "user", Content: session.Content{session.NewTextPart("hi")}},
		{Role: "assistant", Content: r.Content, ToolCalls: r.ToolCalls, Reasoning: roundTrip(t, r.Reasoning)},
		{Role: "tool", ToolCallID: "k1", Content: session.Content{session.NewTextPart("内容")}},
	}}}
	b, _ := json.Marshal(responsesBody(req2, modelhub.Endpoint{Model: "gpt-x"}))
	s := string(b)
	for _, want := range []string{`"encrypted_content":"ENC"`, `"call_id":"call_9","name":"read_file","type":"function_call"`, `"call_id":"call_9","output":"内容","type":"function_call_output"`, `"type":"output_text","text":"你好"`} {
		if !strings.Contains(strings.ReplaceAll(s, `"arguments":"{\"path\":\"a.txt\"}",`, ""), want) && !strings.Contains(s, want) {
			t.Fatalf("replay body missing %s:\n%s", want, s)
		}
	}
	// 其他协议的推理不回放。
	other := "x"
	req3 := &modelhub.Request{Req: session.Request{Messages: []session.Message{{Role: "assistant", Content: session.Content{session.NewTextPart("a")},
		Reasoning: []session.ReasoningBlock{{Text: &other, Opaque: map[string]any{"protocol": "anthropic-messages", "signature": "sig"}}}}}}}
	b3, _ := json.Marshal(responsesBody(req3, modelhub.Endpoint{}))
	if strings.Contains(string(b3), `"type":"reasoning"`) {
		t.Fatalf("foreign reasoning replayed: %s", b3)
	}
}

// roundTrip 模拟日志持久化：Opaque 经 JSON 变成 map。
func roundTrip(t *testing.T, rbs []session.ReasoningBlock) []session.ReasoningBlock {
	t.Helper()
	b, err := json.Marshal(rbs)
	if err != nil {
		t.Fatal(err)
	}
	var out []session.ReasoningBlock
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestResponsesFailures(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   modelhub.Code
	}{
		{"failed-overflow", 200, sse(`{"type":"response.failed","response":{"status":"failed","error":{"code":"context_length_exceeded","message":"too long"}}}`), modelhub.CodeContextOverflow},
		{"error-rate", 200, sse(`{"type":"error","code":"rate_limit_exceeded","message":"slow down"}`), modelhub.CodeRateLimit},
		{"truncated", 200, sse(`{"type":"response.output_text.delta","output_index":0,"delta":"半"}`), modelhub.CodeTransport},
		{"empty", 200, sse(`{"type":"response.completed","response":{"status":"completed"}}`), modelhub.CodeEmptyResponse},
		{"http-auth", 401, `{"error":{"message":"bad key"}}`, modelhub.CodeAuth},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := serve(t, c.status, nil, c.body, nil)
			defer srv.Close()
			req := &modelhub.Request{Req: session.Request{Messages: []session.Message{{Role: "user", Content: session.Content{session.NewTextPart("hi")}}}}}
			_, term := collectOne(t, OpenAIResponses{}.Stream(context.Background(), req, modelhub.Endpoint{BaseURL: srv.URL, Model: "m"}))
			if term.Failed == nil || term.Failed.Code != c.want {
				t.Fatalf("want %s, got %+v / %+v", c.want, term.Failed, term.Done)
			}
		})
	}
}

func TestResponsesIncompleteLength(t *testing.T) {
	body := sse(`{"type":"response.output_text.delta","output_index":0,"delta":"部分"}`,
		`{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`)
	srv := serve(t, 200, nil, body, nil)
	defer srv.Close()
	req := &modelhub.Request{Req: session.Request{Messages: []session.Message{{Role: "user", Content: session.Content{session.NewTextPart("hi")}}}}}
	_, term := collectOne(t, OpenAIResponses{}.Stream(context.Background(), req, modelhub.Endpoint{BaseURL: srv.URL, Model: "m"}))
	if term.Done == nil || term.Done.Stop.V != "length" || modelhub.ContentString(term.Done.Reply.Content) != "部分" {
		t.Fatalf("got %+v / %+v", term.Done, term.Failed)
	}
}
