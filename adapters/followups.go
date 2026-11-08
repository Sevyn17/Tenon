package adapters

// 建议追问（appapi.Followups，计划/05 F9）：按一个回合的输入与回答，用会话的模型侧调用（不经层、不发增量、
// 不进会话日志）生成至多 4 条用户可能接着说的话。失败或输出不合规时返回空，不报错给界面（建议可有可无）。

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"tenon/runtime/assemble"
	"tenon/runtime/session"
)

// FollowupTimeout 一次生成最多等多久。
var FollowupTimeout = 20 * time.Second

const followupPrompt = `下面是用户与智能体的一轮对话。请给出用户最可能接着说的 2 到 4 句话（用用户的口吻、用对话的语言，每句不超过 30 个字，具体、可直接发送，不要重复已经问过的）。
只输出一个 JSON 字符串数组，不要任何解释。如果没有合适的追问，输出 []。`

// FollowupsFrom 经 m 的模型目录做侧调用。model 为空时用缺省模型。
func FollowupsFrom(m *assemble.Models) func(ctx context.Context, model, input, answer string) ([]string, error) {
	return func(ctx context.Context, model, input, answer string) ([]string, error) {
		if model == "" {
			model = m.Model
		}
		ctx, cancel := context.WithTimeout(ctx, FollowupTimeout)
		defer cancel()
		maxTok := uint64(300)
		req := session.Request{
			System: []session.SystemBlock{{Text: followupPrompt, Source: "followups"}},
			Messages: []session.Message{{Role: "user", Content: session.Content{session.NewTextPart(
				"【用户】\n" + input + "\n\n【智能体】\n" + answer)}}},
			Params: session.Params{MaxTokens: &maxTok},
		}
		done, fail := m.Hub.SideCall(ctx, model, req)
		if fail != nil {
			return nil, nil // 建议可有可无：失败当作没有
		}
		var sb strings.Builder
		for _, p := range done.Reply.Content {
			if p.Kind == "text" {
				sb.WriteString(p.Text)
			}
		}
		return parseFollowups(sb.String()), nil
	}
}

// parseFollowups 从模型输出里取 JSON 字符串数组（容忍前后多余文字、代码块围栏）；去空、去重、截断，最多 4 条。
func parseFollowups(s string) []string {
	i, j := strings.Index(s, "["), strings.LastIndex(s, "]")
	if i < 0 || j <= i {
		return []string{}
	}
	var raw []string
	if err := json.Unmarshal([]byte(s[i:j+1]), &raw); err != nil {
		return []string{}
	}
	out, seen := []string{}, map[string]bool{}
	for _, x := range raw {
		x = strings.TrimSpace(strings.ReplaceAll(x, "\n", " "))
		if x == "" || seen[x] {
			continue
		}
		if utf8.RuneCountInString(x) > 80 {
			r := []rune(x)
			x = string(r[:80]) + "…"
		}
		seen[x] = true
		out = append(out, x)
		if len(out) == 4 {
			break
		}
	}
	return out
}
