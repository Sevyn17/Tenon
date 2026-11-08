package responses

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/runtime/modelhub"
	"github.com/Sevyn17/tenon/runtime/session"
)

// 支持图片的端点：user 消息与函数结果都用 input_image（data URL）；函数结果带图片时 output 写成数组
// （2026-11-05：原先图片一律变成占位文字）。
func TestResponsesImages(t *testing.T) {
	img := session.Part{Kind: "media", Media: &session.MediaRef{URI: "artifact://h", MIME: "image/png", Bytes: 3}}
	req := &modelhub.Request{Media: func(string) ([]byte, error) { return []byte{1, 2, 3}, nil },
		Req: session.Request{Model: "m", Messages: []session.Message{
			{Role: "user", Content: session.Content{session.NewTextPart("看"), img}},
			{Role: "assistant", ToolCalls: []session.ToolCall{{ID: "c1", Name: "shot", Args: json.RawMessage(`{}`)}}},
			{Role: "tool", ToolCallID: "c1", Content: session.Content{session.NewTextPart("截图"), img}},
			{Role: "user", Content: session.Content{session.NewTextPart("纯文本")}},
		}}}
	raw, _ := json.Marshal(responsesBody(req, modelhub.Endpoint{Model: "gpt", Images: true}))
	var body struct {
		Input []map[string]json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if c := string(body.Input[0]["content"]); !strings.Contains(c, `"type":"input_image"`) || !strings.Contains(c, "data:image/png;base64,AQID") {
		t.Fatalf("user 消息应带 input_image：%s", c)
	}
	if o := string(body.Input[2]["output"]); !strings.HasPrefix(o, "[") || !strings.Contains(o, `"input_image"`) || !strings.Contains(o, `"截图"`) {
		t.Fatalf("函数结果带图片时 output 应为数组：%s", o)
	}
	if c := string(body.Input[3]["content"]); c != `[{"type":"input_text","text":"纯文本"}]` {
		t.Fatalf("纯文本消息格式应不变：%s", c)
	}

	raw, _ = json.Marshal(responsesBody(req, modelhub.Endpoint{Model: "deepseek"}))
	if strings.Contains(string(raw), "input_image") || !strings.Contains(string(raw), "未发送") {
		t.Fatalf("不支持图片的端点应留占位文字：%s", raw)
	}
}
