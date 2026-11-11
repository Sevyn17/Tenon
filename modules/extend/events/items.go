package events

// 权限项与分发工具（T5b）。

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// ItemDispatch 权限项 id：把事件分发给另一个 agent / resident。
const ItemDispatch = "event.dispatch"

// Items 本插件声明的权限项。
func Items() []toolhub.Item {
	collab := map[string]toolhub.ItemAction{
		"read-only": toolhub.ActionDeny,
		"ask":       toolhub.ActionAsk,
		"workspace": toolhub.ActionAllow,
		"full":      toolhub.ActionAllow,
	}
	return []toolhub.Item{
		{ID: ItemDispatch, Title: "分发外部事件给别的智能体", Type: toolhub.ItemSwitch, Class: toolhub.EffectCollab, Suggest: collab},
	}
}

// dispatchTool 会话工具 event_dispatch {to, body}：把事件转发给 to（agent:<名> / resident:<名>）。
// 供 Router 定义的智能体在回合里分发外部事件给 Worker。
func dispatchTool(hub *Hub) *toolhub.Tool {
	return &toolhub.Tool{
		Spec: session.ToolSpec{Name: "event_dispatch",
			Description: "把一条外部事件分发给另一个智能体（Router 用）。to 是目标地址：agent:<定义名>（每次新建会话）或 resident:<定义名>（常驻会话）。",
			Schema: json.RawMessage(`{"type":"object","properties":{` +
				`"to":{"type":"string","description":"目标地址，如 agent:worker 或 resident:worker"},` +
				`"body":{"type":"string","description":"事件正文"}},` +
				`"required":["to","body"]}`)},
		Risk:  toolhub.Write,
		Items: []string{ItemDispatch},
		Call: func(ctx context.Context, cx *toolhub.Cx, raw json.RawMessage) (toolhub.Output, error) {
			var a struct{ To, Body string }
			if err := json.Unmarshal(raw, &a); err != nil || strings.TrimSpace(a.To) == "" {
				return toolhub.Failf("to 不能为空"), nil
			}
			if err := hub.Post(ctx, strings.TrimSpace(a.To), a.Body); err != nil {
				return toolhub.Failf("分发失败：%v", err), nil
			}
			return toolhub.Text("已分发"), nil
		},
	}
}
