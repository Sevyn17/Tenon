// Package askuser 提供 ask_user 工具：让模型向人提问并等回答。
// 没有可应答者时工具报错，模型据此换做法。
package askuser

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// Asker 向人提问；返回回答。nil = 无人可问。
type Asker func(ctx context.Context, question string, options []string) (string, error)

// SessionAsker 带会话 id 的提问（多会话界面据此路由，审查 P1-28）。
type SessionAsker func(ctx context.Context, sessionID, question string, options []string) (string, error)

// AskUserTimeout ask_user 的默认等待上限（与确认挂起一致）。
const AskUserTimeout = 24 * time.Hour

// AskUser 阻塞式提问工具（不带会话 id）。
func AskUser(ask Asker) *toolhub.Tool {
	var sa SessionAsker
	if ask != nil {
		sa = func(ctx context.Context, _ string, q string, o []string) (string, error) { return ask(ctx, q, o) }
	}
	return AskUserIn(sa)
}

// AskUserIn 同 AskUser，提问带上发起调用的会话 id。
func AskUserIn(ask SessionAsker) *toolhub.Tool {
	no := false
	t := toolhub.Simple("ask_user",
		"需求有歧义或决定代价大时向用户提问。options 可给出选项。能自行合理决定的不要问。",
		`{"type":"object","properties":{"question":{"type":"string"},"options":{"type":"array","items":{"type":"string"}}},"required":["question"]}`, nil)
	t.Call = func(ctx context.Context, cx *toolhub.Cx, raw json.RawMessage) (toolhub.Output, error) {
		s, err := func() (string, error) {
			var a struct {
				Question string
				Options  []string
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", err
			}
			if strings.TrimSpace(a.Question) == "" {
				return "", errors.New("question 不能为空")
			}
			if ask == nil {
				return "", errors.New("当前无人可问，请自行决定并在回答中说明假设")
			}
			sid := ""
			if cx != nil {
				sid = cx.SessionID
			}
			ans, err := ask(ctx, sid, a.Question, a.Options)
			if err != nil {
				return "", err
			}
			b, _ := json.Marshal(map[string]string{"answer": ans})
			return string(b), nil
		}()
		if err != nil {
			return toolhub.Failf("%v", err), nil
		}
		return toolhub.Text(s), nil
	}
	t.Parallel = &no
	t.Timeout = AskUserTimeout
	// 行为就是“问用户”，再加一层确认只会变成两次询问：显式声明无需授权（§9.3）。
	t.NoAuth = true
	return t
}

// Interactor 结构化交互（assemble.Interact 的形状）：返回回答的 JSON 值。nil 或界面不支持时 ask_choice 不出现。
type Interactor func(ctx context.Context, sessionID, kind, title string, body map[string]any) (json.RawMessage, error)

// AskChoice 让用户从给定选项里选（单选或多选），回答是选中项的 id。比 ask_user 的 options 更结构化：
// 选项带说明、可多选、界面按卡片呈现，结果不会被用户改写成自由文本。
func AskChoice(ask Interactor) *toolhub.Tool {
	no := false
	t := toolhub.Simple("ask_choice",
		"让用户从几个方案里选（可多选）。每个选项给 id、label，可加 description。只在有几个明确方案、需要用户拍板时用；开放问题用 ask_user。",
		`{"type":"object","properties":{
"question":{"type":"string"},
"options":{"type":"array","minItems":2,"items":{"type":"object","properties":{"id":{"type":"string"},"label":{"type":"string"},"description":{"type":"string"}},"required":["id","label"]}},
"multiple":{"type":"boolean","description":"可多选"}},"required":["question","options"]}`, nil)
	t.Call = func(ctx context.Context, cx *toolhub.Cx, raw json.RawMessage) (toolhub.Output, error) {
		var a struct {
			Question string `json:"question"`
			Options  []struct {
				ID          string `json:"id"`
				Label       string `json:"label"`
				Description string `json:"description,omitempty"`
			} `json:"options"`
			Multiple bool `json:"multiple"`
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			return toolhub.Failf("参数不合法：%v", err), nil
		}
		if strings.TrimSpace(a.Question) == "" || len(a.Options) < 2 {
			return toolhub.Failf("需要 question 与至少 2 个选项"), nil
		}
		seen := map[string]bool{}
		for _, o := range a.Options {
			if o.ID == "" || o.Label == "" || seen[o.ID] {
				return toolhub.Failf("每个选项要有不重复的 id 和 label"), nil
			}
			seen[o.ID] = true
		}
		sid := ""
		if cx != nil {
			sid = cx.SessionID
		}
		body := map[string]any{"text": a.Question, "options": a.Options, "multiple": a.Multiple}
		v, err := ask(ctx, sid, "choice", a.Question, body)
		if err != nil {
			return toolhub.Failf("%v", err), nil
		}
		b, _ := json.Marshal(map[string]json.RawMessage{"selected": v})
		return toolhub.Text(string(b)), nil
	}
	t.Parallel = &no
	t.Timeout = AskUserTimeout
	t.NoAuth = true
	return t
}
