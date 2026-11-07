package openaichat

import (
	"encoding/json"
	"strings"
	"testing"

	"tenon/runtime/modelhub"
	"tenon/runtime/session"
)

// 支持图片的端点：user 消息里的图片写成 image_url 段；tool 消息只收文本，工具结果里的图片在这一串
// tool 消息之后补一条 user 消息（2026-11-05：原先图片一律变成占位文字）。不支持图片的端点留占位。
func TestOpenAIChatImages(t *testing.T) {
	img := session.Part{Kind: "media", Media: &session.MediaRef{URI: "artifact://h", MIME: "image/png", Bytes: 3}}
	req := &modelhub.Request{Media: func(string) ([]byte, error) { return []byte{1, 2, 3}, nil },
		Req: session.Request{Model: "m", Messages: []session.Message{
			{Role: "user", Content: session.Content{session.NewTextPart("看"), img}},
			{Role: "assistant", ToolCalls: []session.ToolCall{{ID: "c1", Name: "shot", Args: json.RawMessage(`{}`)},
				{ID: "c2", Name: "shot", Args: json.RawMessage(`{}`)}}},
			{Role: "tool", ToolCallID: "c1", Content: session.Content{session.NewTextPart("截图"), img}},
			{Role: "tool", ToolCallID: "c2", Content: session.Content{session.NewTextPart("无图")}},
		}}}
	raw, _ := json.Marshal(openAIBody(req, modelhub.Endpoint{Model: "gpt", Images: true}))
	var body struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	roles := []string{}
	for _, m := range body.Messages {
		roles = append(roles, m.Role)
	}
	if got := strings.Join(roles, ","); got != "user,assistant,tool,tool,user" {
		t.Fatalf("工具结果的图片应在两条 tool 消息之后补一条 user：%s", got)
	}
	first, last := string(body.Messages[0].Content), string(body.Messages[4].Content)
	for _, c := range []string{first, last} {
		if !strings.Contains(c, `"type":"image_url"`) || !strings.Contains(c, "data:image/png;base64,AQID") {
			t.Fatalf("应带 image_url：%s", c)
		}
	}
	if strings.Contains(string(raw), "未发送") {
		t.Fatalf("支持图片时不应留占位：%s", raw)
	}

	raw, _ = json.Marshal(openAIBody(req, modelhub.Endpoint{Model: "deepseek"}))
	if strings.Contains(string(raw), "image_url") || !strings.Contains(string(raw), "未发送") {
		t.Fatalf("不支持图片的端点应留占位文字：%s", raw)
	}
}
