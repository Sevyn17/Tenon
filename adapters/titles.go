package adapters

// 会话短标题（appapi.Titler）：按第一条用户消息，用模型侧调用（不经层、不进会话日志）起一个短标题。
// 失败或输出不合规时返回空，界面保留兜底标题（第一行）。

import (
	"context"
	"strings"

	"tenon/runtime/assemble"
	"tenon/runtime/session"
)

const titlePrompt = `给下面这条用户消息起一个会话标题：概括用户要做的事，用消息的语言，中文不超过 12 个字、英文不超过 6 个词，不加引号、句号或前缀。
只输出标题本身。`

// TitlerFrom 经 m 的模型目录做侧调用。model 为空时用缺省模型。
func TitlerFrom(m *assemble.Models) func(ctx context.Context, model, input string) (string, error) {
	return func(ctx context.Context, model, input string) (string, error) {
		if model == "" {
			model = m.Model
		}
		maxTok := uint64(40)
		req := session.Request{
			System:   []session.SystemBlock{{Text: titlePrompt, Source: "titles"}},
			Messages: []session.Message{{Role: "user", Content: session.Content{session.NewTextPart(input)}}},
			Params:   session.Params{MaxTokens: &maxTok},
		}
		done, fail := m.Hub.SideCall(ctx, model, req)
		if fail != nil {
			return "", nil
		}
		var sb strings.Builder
		for _, p := range done.Reply.Content {
			if p.Kind == "text" {
				sb.WriteString(p.Text)
			}
		}
		return cleanTitle(sb.String()), nil
	}
}

// cleanTitle 去掉模型常加的引号、"标题："前缀和句末标点。
func cleanTitle(s string) string {
	s = strings.TrimSpace(strings.SplitN(strings.TrimSpace(s), "\n", 2)[0])
	for _, p := range []string{"标题：", "标题:", "Title:", "title:"} {
		s = strings.TrimSpace(strings.TrimPrefix(s, p))
	}
	s = strings.Trim(s, "\"'“”‘’「」《》`*")
	return strings.TrimRight(s, "。.！!")
}
