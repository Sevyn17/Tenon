package openaichat

// openai-chat 适配器测试（M3.11）：解析、恰好一个终止事件、稳定失败 code、缓存用量归一（G5）。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ybase/runtime/modelhub"
	"ybase/runtime/session"
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

func TestOpenAIChatParseAndCacheUsage(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"choices":[{"delta":{"reasoning_content":"想一想"}}]}`,
		`data: {"choices":[{"delta":{"content":"你"}}]}`,
		`data: {"choices":[{"delta":{"content":"好","tool_calls":[{"index":0,"id":"call_a","function":{"name":"echo","arguments":"{\"s\":"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]},"finish_reason":"tool_calls"}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":7,"prompt_cache_hit_tokens":80,"prompt_cache_miss_tokens":20}}`,
		`data: [DONE]`, ``}, "\n\n")
	var seen map[string]any
	srv := serve(t, 200, nil, sse, &seen)
	defer srv.Close()
	ep := modelhub.Endpoint{BaseURL: srv.URL, Model: "deepseek-chat", Compat: modelhub.Compat{PromptCacheKey: true}}
	text, term := collectOne(t, OpenAIChat{}.Stream(context.Background(), baseReq(), ep))
	if text != "你好" || term.Done == nil {
		t.Fatalf("text=%q term=%+v", text, term)
	}
	r := term.Done.Reply
	if len(r.ToolCalls) != 1 || r.ToolCalls[0].WireID != "call_a" || string(r.ToolCalls[0].Args) != `{"s":1}` {
		t.Fatalf("工具调用: %+v", r.ToolCalls)
	}
	if len(r.Reasoning) != 1 || *r.Reasoning[0].Text != "想一想" {
		t.Fatalf("推理: %+v", r.Reasoning)
	}
	if r.Usage.Input != 100 || r.Usage.CacheRead != 80 || r.Usage.Source != "Reported" {
		t.Fatalf("DeepSeek 缓存用量未归一: %+v", r.Usage)
	}
	if seen["prompt_cache_key"] != "sess-key" {
		t.Fatalf("未带会话缓存亲和 id: %v", seen["prompt_cache_key"])
	}
}

func TestOpenAIChatMissingUsageAndErrors(t *testing.T) {
	sse := "data: {\"choices\":[{\"delta\":{\"content\":\"x\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	srv := serve(t, 200, nil, sse, nil)
	defer srv.Close()
	_, term := collectOne(t, OpenAIChat{}.Stream(context.Background(), baseReq(), modelhub.Endpoint{BaseURL: srv.URL}))
	if term.Done == nil || term.Done.Reply.Usage.Source != "Missing" {
		t.Fatalf("未报用量应标 Missing: %+v", term)
	}

	cases := []struct {
		status int
		body   string
		hdr    map[string]string
		code   modelhub.Code
	}{
		{401, `{"error":"bad key"}`, nil, modelhub.CodeAuth},
		{429, `{"error":"slow down"}`, map[string]string{"Retry-After": "3"}, modelhub.CodeRateLimit},
		{400, `{"error":{"message":"This model's maximum context length is 8192 tokens"}}`, nil, modelhub.CodeContextOverflow},
		{503, `oops`, nil, modelhub.CodeServer},
	}
	for _, c := range cases {
		srv := serve(t, c.status, c.hdr, c.body, nil)
		_, term := collectOne(t, OpenAIChat{}.Stream(context.Background(), baseReq(), modelhub.Endpoint{BaseURL: srv.URL}))
		srv.Close()
		if term.Failed == nil || term.Failed.Code != c.code {
			t.Fatalf("http %d → %+v，期望 %s", c.status, term.Failed, c.code)
		}
		if c.code == modelhub.CodeRateLimit && term.Failed.RetryAfter.Seconds() != 3 {
			t.Fatalf("Retry-After 未解析: %v", term.Failed.RetryAfter)
		}
	}

	// 流中途断开（无 [DONE] 也无 finish_reason）→ 传输错误，带已收到的部分。
	srv2 := serve(t, 200, nil, "data: {\"choices\":[{\"delta\":{\"content\":\"半\"}}]}\n\n", nil)
	defer srv2.Close()
	_, term = collectOne(t, OpenAIChat{}.Stream(context.Background(), baseReq(), modelhub.Endpoint{BaseURL: srv2.URL}))
	if term.Failed == nil || term.Failed.Code != modelhub.CodeTransport {
		t.Fatalf("断流: %+v", term)
	}
}
