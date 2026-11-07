package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"tenon/runtime/modelhub"
	"tenon/runtime/session"
)

// 2026-11-04 审查 P2 工具体验：失败的工具结果带 is_error；max_tokens 取模型目录的 max_output；
// 图片等非文本内容不再静默丢弃，换成占位文字让模型知道有东西没发过去。
func TestAnthropicBodyUX(t *testing.T) {
	req := &modelhub.Request{Req: session.Request{Model: "m", Messages: []session.Message{
		{Role: "user", Content: session.Content{session.NewTextPart("看图"), {Kind: "image", Media: &session.MediaRef{MIME: "image/png"}}}},
		{Role: "assistant", ToolCalls: []session.ToolCall{{ID: "c1", Name: "x", Args: json.RawMessage(`{}`)}}},
		{Role: "tool", ToolCallID: "c1", Content: session.Content{session.NewTextPart("坏了")}, IsError: true},
	}}}
	raw, _ := json.Marshal(anthropicBody(req, modelhub.Endpoint{Model: "claude", MaxOutput: 32000}))
	s := string(raw)
	if !strings.Contains(s, `"is_error":true`) {
		t.Errorf("失败的工具结果应带 is_error：%s", s)
	}
	if !strings.Contains(s, `"max_tokens":32000`) {
		t.Errorf("max_tokens 应取目录的 max_output：%s", s)
	}
	if !strings.Contains(s, "image/png") {
		t.Errorf("图片应留占位文字：%s", s)
	}
}
