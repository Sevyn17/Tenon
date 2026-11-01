package subagent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"ybase-kernel/core/host"
	"ybase-kernel/core/session"
	"ybase-kernel/core/toolhub"
)

// SubagentTimeout 委派工具默认超时（子回合可能很长）。
const SubagentTimeout = 30 * time.Minute

// SubagentTool 模型面委派工具 `subagent {task, session?}`：同步跑子会话一个回合，返回其最终文本。
// 子会话 id = <父 id>/sub-<调用 id 末 8 位（uuid v7 末段随机）>；传 session 续用已有子会话（必须是本会话的子智能体）。
// f 构造子会话的 agent（不含 subagent 工具 → 深度 1）。
func Tool(h *host.Host, f host.Factory) *toolhub.Tool {
	no := false
	return &toolhub.Tool{
		Spec: session.ToolSpec{Name: "subagent",
			Description: "把一个独立、边界清楚的子任务委派给子智能体（全新上下文，同样的工具）。task 要自包含：子智能体看不到当前对话。" +
				"返回子智能体的最终回答与子会话 id；传 session 可继续同一个子会话追问。",
			Schema: json.RawMessage(`{"type":"object","properties":{"task":{"type":"string","description":"自包含的任务说明"},` +
				`"session":{"type":"string","description":"可选：续用的子会话 id"}},"required":["task"]}`)},
		Risk:     toolhub.Write,
		Parallel: &no,
		Timeout:  SubagentTimeout,
		Attrs:    map[string]any{toolhub.AttrEffect: toolhub.EffectSpawn},
		Items:    []string{ItemSpawn},
		Call: func(ctx context.Context, cx *toolhub.Cx, raw json.RawMessage) (toolhub.Output, error) {
			var a struct{ Task, Session string }
			if err := json.Unmarshal(raw, &a); err != nil || strings.TrimSpace(a.Task) == "" {
				return toolhub.Errorf("task 不能为空"), nil
			}
			id := a.Session
			if id == "" {
				cid := cx.CallID
				if len(cid) > 8 {
					cid = cid[len(cid)-8:]
				}
				id = cx.SessionID + "/sub-" + cid
			}
			cx.ChildSessionID = id
			out, err := h.Spawn(ctx, cx.SessionID, id, f, a.Task)
			if err != nil {
				if ctx.Err() != nil {
					return toolhub.Output{}, ctx.Err()
				}
				return toolhub.Errorf("子智能体 %s 失败：%v", id, err), nil
			}
			return toolhub.Text(fmt.Sprintf("[子会话 %s]\n%s", id, out)), nil
		},
	}
}
