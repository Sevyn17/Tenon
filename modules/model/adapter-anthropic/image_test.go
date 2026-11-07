package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"tenon/runtime/modelhub"
	"tenon/runtime/session"
)

// 支持图片的端点：工具结果里的媒体引用按 Request.Media 取回，发成 image 块（2026-11-04 审查 P2：原先一律丢弃）；
// 不支持图片的端点照旧留占位文字。
func TestAnthropicSendsImages(t *testing.T) {
	img := session.Part{Kind: "media", Media: &session.MediaRef{URI: "artifact://h1", MIME: "image/png", Bytes: 3}}
	req := &modelhub.Request{Req: session.Request{Model: "m", Messages: []session.Message{
		{Role: "user", Content: session.Content{session.NewTextPart("看")}},
		{Role: "assistant", ToolCalls: []session.ToolCall{{ID: "c1", Name: "shot", Args: json.RawMessage(`{}`)}}},
		{Role: "tool", ToolCallID: "c1", Content: session.Content{session.NewTextPart("截图"), img}},
	}}, Media: func(uri string) ([]byte, error) { return []byte("PNG"), nil }}
	raw, _ := json.Marshal(anthropicBody(req, modelhub.Endpoint{Model: "claude", Images: true}))
	s := string(raw)
	if !strings.Contains(s, `"type":"image"`) || !strings.Contains(s, `"data":"UE5H"`) || !strings.Contains(s, `"media_type":"image/png"`) {
		t.Fatalf("支持图片的端点应发 image 块：%s", s)
	}
	raw, _ = json.Marshal(anthropicBody(req, modelhub.Endpoint{Model: "claude"}))
	if s := string(raw); strings.Contains(s, `"type":"image"`) || !strings.Contains(s, "未发送") {
		t.Fatalf("不支持图片的端点应留占位：%s", s)
	}
}
