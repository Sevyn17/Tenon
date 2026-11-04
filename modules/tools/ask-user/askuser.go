package askuser

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"ybase/runtime/toolhub"
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
			return toolhub.Errorf("%v", err), nil
		}
		return toolhub.Text(s), nil
	}
	t.Parallel = &no
	t.Timeout = AskUserTimeout
	t.WithEffect(toolhub.EffectInteract)
	// 行为就是“问用户”，再加一层确认只会变成两次询问：显式声明无需授权（§9.3）。
	t.NoAuth = true
	return t
}
