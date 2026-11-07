package mcp

import (
	"encoding/json"
	"testing"

	"tenon/runtime/session"
)

// MCP 返回的图片存成工件、以媒体片段进工具结果（2026-11-04 审查 P2：原先只留"未显示"）；存不了时留占位。
func TestRenderResultKeepsImages(t *testing.T) {
	res := json.RawMessage(`{"content":[{"type":"text","text":"截图"},{"type":"image","mimeType":"image/png","data":"UE5H"}]}`)
	var got []byte
	out := renderResult(res, func(mime string, data []byte) (session.Part, bool) {
		got = data
		return session.Part{Kind: "media", Media: &session.MediaRef{URI: "artifact://h", MIME: mime}}, true
	})
	if string(got) != "PNG" {
		t.Fatalf("应按 base64 解出图片内容：%q", got)
	}
	var media int
	for _, p := range out.Content {
		if p.Kind == "media" && p.Media != nil && p.Media.MIME == "image/png" {
			media++
		}
	}
	if media != 1 {
		t.Fatalf("结果应带一个媒体片段：%+v", out.Content)
	}
	out = renderResult(res, nil)
	if len(out.Content) != 1 || out.Content[0].Kind != "text" {
		t.Fatalf("存不了时只留文本占位：%+v", out.Content)
	}
}
