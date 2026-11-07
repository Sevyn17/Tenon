package openaichat

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"tenon/runtime/modelhub"
	"tenon/runtime/session"
)

// 工具调用的流式片段经 StreamSink 送出（protocol 2 tool.started / tool.args_delta）：名字只在第一片，
// 参数片段拼起来等于最终调用的参数。
func TestToolDeltasStreamed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"write_file","arguments":""}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.txt\"}"}}]},"finish_reason":"tool_calls"}]}

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
	hub := modelhub.New(cat, func(string) (string, error) { return "k", nil }, nil)
	if err := hub.RegisterAdapter(OpenAIChat{}); err != nil {
		t.Fatal(err)
	}
	s := &modelhub.Session{Hub: hub, SessionKey: "k"}
	var names []string
	var args strings.Builder
	reply, _, err := s.StreamSink(context.Background(), "r1", session.Request{Model: "p/m",
		Messages: []session.Message{{Role: "user", Content: session.Content{session.NewTextPart("写文件")}}}},
		modelhub.Sink{Tool: func(d modelhub.ToolDelta) {
			if d.Index != 0 {
				t.Errorf("片段 index 应为 0：%d", d.Index)
			}
			if d.Name != "" {
				names = append(names, d.Name)
			}
			args.WriteString(d.Args)
		}})
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "write_file" {
		t.Fatalf("名字应只送一次：%v", names)
	}
	if len(reply.ToolCalls) != 1 || args.String() != string(reply.ToolCalls[0].Args) {
		t.Fatalf("参数片段拼起来应等于最终参数：%q vs %+v", args.String(), reply.ToolCalls)
	}
}
