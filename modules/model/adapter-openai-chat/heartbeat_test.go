package openaichat

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ybase/runtime/modelhub"
	"ybase/runtime/session"
)

// 只流工具参数、不出文本，总时长超过调度器的首事件超时：适配器要发心跳，调用成功不重试
// （2026-11-04 审查 P0-13：原先被判首事件超时，按可重试码重试，最后失败）。
func TestToolArgsStreamKeepsAlive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		io.WriteString(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"write_file","arguments":""}}]}}]}

`)
		fl.Flush()
		for i := 0; i < 8; i++ { // 8 × 60ms ≈ 480ms > FirstEvent 200ms
			time.Sleep(60 * time.Millisecond)
			io.WriteString(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":" "}}]}}]}

`)
			fl.Flush()
		}
		io.WriteString(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]},"finish_reason":"tool_calls"}]}

data: [DONE]

`)
	}))
	defer srv.Close()
	cat := modelhub.NewCatalog()
	if err := cat.Add(modelhub.LayerUser, "t", modelhub.CatalogFile{
		Provider: map[string]modelhub.Provider{"p": {Protocol: "openai-chat", BaseURL: srv.URL}},
		Model:    map[string]modelhub.Model{"p/m": {Provider: "p"}},
	}); err != nil {
		t.Fatal(err)
	}
	pol := modelhub.DefaultPolicy
	pol.FirstEvent, pol.Idle = 200*time.Millisecond, 200*time.Millisecond
	hub := modelhub.New(cat, func(string) (string, error) { return "k", nil }, &pol)
	if err := hub.RegisterAdapter(OpenAIChat{}); err != nil {
		t.Fatal(err)
	}
	s := &modelhub.Session{Hub: hub, SessionKey: "k"}
	reply, _, err := s.StreamRequest(context.Background(), "r1", session.Request{Model: "p/m",
		Messages: []session.Message{{Role: "user", Content: session.Content{session.NewTextPart("写文件")}}}}, nil, nil)
	if err != nil {
		t.Fatalf("长工具参数流不应超时: %v", err)
	}
	if len(reply.ToolCalls) != 1 {
		t.Fatalf("应得到一个工具调用: %+v", reply)
	}
}
