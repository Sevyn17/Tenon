package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/runtime/modelhub"
	"github.com/Sevyn17/tenon/runtime/session"
)

// 缓存断点：工具末尾、系统末块、最后两条 user 消息各一个，合计不超过厂商上限 4
// （2026-11-04 审查 P1-46：原先只有最后一条 user 消息打断点）。
func TestCacheBreakpoints(t *testing.T) {
	user := func(s string) session.Message {
		return session.Message{Role: "user", Content: session.Content{session.NewTextPart(s)}}
	}
	asst := func(s string) session.Message {
		return session.Message{Role: "assistant", Content: session.Content{session.NewTextPart(s)}}
	}
	r := &modelhub.Request{Req: session.Request{
		System:   []session.SystemBlock{{Text: "a", Breakpoint: true}, {Text: "b", Breakpoint: true}, {Text: "c"}},
		Messages: []session.Message{user("u1"), asst("a1"), user("u2"), asst("a2"), user("u3")},
		Tools:    []session.ToolSpec{{Name: "x", Schema: json.RawMessage(`{"type":"object"}`)}, {Name: "y", Schema: json.RawMessage(`{"type":"object"}`)}},
	}}
	raw, _ := json.Marshal(anthropicBody(r, modelhub.Endpoint{Model: "claude"}))
	s := string(raw)
	if n := strings.Count(s, "cache_control"); n != anthropicMaxBreakpoints {
		t.Fatalf("断点数 = %d，期望 %d：%s", n, anthropicMaxBreakpoints, s)
	}
	var b struct {
		System   []map[string]any `json:"system"`
		Messages []struct {
			Content []map[string]any `json:"content"`
		} `json:"messages"`
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	has := func(m map[string]any) bool { _, ok := m["cache_control"]; return ok }
	if !has(b.Tools[len(b.Tools)-1]) || has(b.Tools[0]) {
		t.Fatalf("工具末尾应打断点：%v", b.Tools)
	}
	if !has(b.System[len(b.System)-1]) {
		t.Fatalf("系统末块应打断点：%v", b.System)
	}
	last := func(i int) map[string]any { c := b.Messages[i].Content; return c[len(c)-1] }
	if !has(last(4)) || !has(last(2)) || has(last(0)) {
		t.Fatalf("应滚动打在最后两条 user 消息：%s", s)
	}
}
