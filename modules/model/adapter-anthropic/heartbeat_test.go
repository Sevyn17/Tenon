package anthropic

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/runtime/modelhub"
	"github.com/Sevyn17/tenon/runtime/session"
)

// 只流工具参数、不出文本，总时长超过调度器的首事件超时：适配器要发心跳，调用成功不重试
// （2026-11-04 审查 P0-13：原先被判首事件超时，按可重试码重试，最后失败）。
func TestToolArgsStreamKeepsAlive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		io.WriteString(w, `event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tu_1","name":"write_file"}}

`)
		fl.Flush()
		for i := 0; i < 8; i++ { // 8 × 60ms ≈ 480ms > FirstEvent 200ms
			time.Sleep(60 * time.Millisecond)
			io.WriteString(w, `event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":" "}}

`)
			fl.Flush()
		}
		io.WriteString(w, `event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":4}}

event: message_stop
data: {"type":"message_stop"}

`)
	}))
	defer srv.Close()
	cat := modelhub.NewCatalog()
	if err := cat.Add(modelhub.LayerUser, "t", modelhub.CatalogFile{
		Provider: map[string]modelhub.Provider{"p": {Protocol: "anthropic-messages", BaseURL: srv.URL}},
		Model:    map[string]modelhub.Model{"p/m": {Provider: "p"}},
	}); err != nil {
		t.Fatal(err)
	}
	pol := modelhub.DefaultPolicy
	pol.FirstEvent, pol.Idle = 200*time.Millisecond, 200*time.Millisecond
	hub := modelhub.New(cat, func(string) (string, error) { return "k", nil }, &pol)
	if err := hub.RegisterAdapter(Anthropic{}); err != nil {
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
