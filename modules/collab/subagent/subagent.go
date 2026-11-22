// Package subagent 提供委派工具：把一段任务派给子智能体（独立子会话），
// 等它完成后把结果交回父会话。
package subagent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Sevyn17/tenon/runtime/host"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/tasks"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// SubagentTimeout 委派工具默认超时（子回合可能很长）。
const SubagentTimeout = 30 * time.Minute

// Params 委派工具开放哪些按次参数（Mortise docs/62 K5、docs/56 第 2 节）：开着的才出现在工具 schema 里；
// 关着的取缺省行为（只用基础提示词 / 跟随主会话模型 / 继承主会话档位 / 继承主会话工具集）。task 必有，不在这里。
type Params struct {
	SystemPrompt bool
	Model        bool
	Mode         bool
	Tools        bool
}

// SubagentTool 模型面委派工具 `subagent {task, session?}`：同步跑子会话一个回合，返回其最终文本。
// 子会话 id = <父 id>/sub-<调用 id 末 8 位（uuid v7 末段随机）>；传 session 续用已有子会话（必须是本会话的子智能体）。
// f 构造子会话的 agent（不含 subagent 工具 → 深度 1）。
func Tool(h *host.Host, f host.Factory) *toolhub.Tool { return ToolWithTasks(h, f, nil) }

// ToolWithTasks 同 Tool，另外在每次委派开始 / 结束时向 sink 登记（Mortise docs/62 K2）；sink 为 nil 不报。
// 不开放按次参数（同 ToolWithParams 的 Params{}）。
func ToolWithTasks(h *host.Host, f host.Factory, sink tasks.Sink) *toolhub.Tool {
	return ToolWithParams(h, f, sink, Params{})
}

// schemaFor 按开关拼工具参数 schema。
func schemaFor(p Params) json.RawMessage {
	props := map[string]any{
		"task":    map[string]any{"type": "string", "description": "自包含的任务说明"},
		"session": map[string]any{"type": "string", "description": "可选：续用的子会话 id（续用时沿用创建时的设置，本次的覆盖参数不生效）"},
		"agent":   map[string]any{"type": "string", "description": "可选：子智能体用的定义名；只能选本会话定义 [use.subagent] agents 里列出的，缺省为通用子智能体"},
	}
	if p.SystemPrompt {
		props["system_prompt"] = map[string]any{"type": "string", "description": "可选：追加在子智能体基础提示词之后的系统提示，用来定角色、定输出格式"}
	}
	if p.Model {
		props["model"] = map[string]any{"type": "string", "description": "可选：子智能体用的模型（模型 id 或别名）；缺省跟随主会话"}
	}
	if p.Mode {
		props["mode"] = map[string]any{"type": "string", "description": "可选：子智能体的权限档位（read-only / ask / workspace / full 或自定义档位）；最多与主会话一样宽，更宽的按主会话档位；缺省继承主会话"}
	}
	if p.Tools {
		props["tools"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"},
			"description": "可选：子智能体可用工具白名单（支持 * ?）；只能是主会话有的工具，其余去掉；缺省继承主会话工具集"}
	}
	b, _ := json.Marshal(map[string]any{"type": "object", "properties": props, "required": []string{"task"}})
	return b
}

// ToolWithParams 同 ToolWithTasks，另按 p 开放按次参数（K5）。参数经 host.SpawnWith 交给会话构造，
// 与父会话上限求交集：mode、tools 只能收窄，收窄时结果里写明「已收窄为 …」，不报错。
func ToolWithParams(h *host.Host, f host.Factory, sink tasks.Sink, p Params) *toolhub.Tool {
	// 一批里的多个委派并行跑（2026-11-04 审查 P2 工具体验：原先串行）；续用同一子会话的调用按 id 串行，见 childLocks。
	yes := true
	var locks childLocks
	return &toolhub.Tool{
		Spec: session.ToolSpec{Name: "subagent",
			Description: "把一个独立、边界清楚的子任务委派给子智能体（全新上下文，同样的工具）。task 要自包含：子智能体看不到当前对话。" +
				"返回子智能体的最终回答与子会话 id；传 session 可继续同一个子会话追问。",
			Schema: schemaFor(p)},
		Risk:     toolhub.Write,
		Parallel: &yes,
		Timeout:  SubagentTimeout,
		Items:    []string{ItemSpawn},
		Call: func(ctx context.Context, cx *toolhub.Cx, raw json.RawMessage) (toolhub.Output, error) {
			var a struct {
				Task, Session, Agent string
				SystemPrompt         string `json:"system_prompt"`
				Model, Mode          string
				Tools                []string
			}
			if err := json.Unmarshal(raw, &a); err != nil || strings.TrimSpace(a.Task) == "" {
				return toolhub.Failf("task 不能为空"), nil
			}
			// 关着的参数即使模型传了也不生效（schema 里没有它）。
			var o host.SpawnOptions
			if p.SystemPrompt {
				o.SystemPrompt = strings.TrimSpace(a.SystemPrompt)
			}
			if p.Model {
				o.Model = strings.TrimSpace(a.Model)
			}
			if p.Mode {
				o.Mode = strings.TrimSpace(a.Mode)
			}
			if p.Tools && a.Tools != nil {
				o.Tools = append([]string{}, a.Tools...)
			}
			var mu sync.Mutex
			var notes []string
			o.OnNarrow = func(n string) {
				mu.Lock()
				notes = append(notes, n)
				mu.Unlock()
			}
			id := a.Session
			if id == "" {
				cid := cx.CallID
				if len(cid) > 8 {
					cid = cid[len(cid)-8:]
				}
				id = cx.SessionID + "/sub-" + cid
			}
			def, err := pickDefinition(cx, strings.TrimSpace(a.Agent))
			if err != nil {
				return toolhub.Failf("%v", err), nil
			}
			cx.ChildSessionID = id
			unlock := locks.lock(id)
			defer unlock()
			t := tasks.Task{ID: id, Session: cx.SessionID, Kind: tasks.KindSubagent, Title: taskTitle(a.Task),
				Status: tasks.Running, Started: time.Now()}
			if sink != nil {
				sink.Started(t)
			}
			out, _, err := h.SpawnWith(ctx, cx.SessionID, id, "subagent", def, f, a.Task, h.Untrusted(cx.SessionID), o)
			if sink != nil {
				t.Ended = time.Now()
				switch {
				case err == nil:
					t.Status, t.Summary = tasks.Done, taskTitle(out)
				case ctx.Err() != nil:
					t.Status, t.Summary = tasks.Stopped, "已取消"
				default:
					t.Status, t.Summary = tasks.Failed, err.Error()
				}
				sink.Ended(t)
			}
			if err != nil {
				if ctx.Err() != nil {
					return toolhub.Output{}, ctx.Err()
				}
				return toolhub.Failf("子智能体 %s 失败：%v", id, err), nil
			}
			head := fmt.Sprintf("[子会话 %s]\n", id)
			mu.Lock()
			for _, n := range notes {
				head += "[" + n + "]\n"
			}
			mu.Unlock()
			return toolhub.Text(head + out), nil
		},
	}
}

// taskTitle 任务标题 / 摘要：取首行，最多 200 字符。
func taskTitle(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200]) + "…"
	}
	return s
}

// childLocks 按子会话 id 串行：两次调用续用同一个子会话时，第二次等第一次的回合结束再投递，
// 不往同一个子会话同时投两条任务（各自 Wait 会等到对方的回合）。
type childLocks struct {
	mu sync.Mutex
	m  map[string]*childLock
}

type childLock struct {
	mu   sync.Mutex
	refs int
}

func (c *childLocks) lock(id string) func() {
	c.mu.Lock()
	if c.m == nil {
		c.m = map[string]*childLock{}
	}
	l := c.m[id]
	if l == nil {
		l = &childLock{}
		c.m[id] = l
	}
	l.refs++
	c.mu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		c.mu.Lock()
		if l.refs--; l.refs == 0 {
			delete(c.m, id)
		}
		c.mu.Unlock()
	}
}

// UseConfig 定义里的 [use.subagent]：本智能体派子智能体时可选的定义（2026-11-04 审查 P2：原先只能用内置的通用定义）。
// 子会话仍不越过父会话（父定义的 tools 白名单与 [use.permission] 上限都叠加到子会话上，见 assemble.ParentLimits）。
//
//	[use.subagent]
//	agents = ["reviewer", "researcher"]
type UseConfig struct {
	Agents []string `toml:"agents" limit:"intersect" description:"subagent 工具的 agent 参数可选的定义名；不写 = 只能用通用子智能体"`
}

// pickDefinition 本次委派用的定义：没给 = 内置通用定义；给了必须在 [use.subagent] agents 里。
func pickDefinition(cx *toolhub.Cx, name string) (string, error) {
	if name == "" || name == Definition {
		return Definition, nil
	}
	var u UseConfig
	if cx.Ctx != nil {
		if err := cx.Ctx.Config("subagent", &u); err != nil {
			return "", err
		}
	}
	for _, a := range u.Agents {
		if a == name {
			return name, nil
		}
	}
	if len(u.Agents) == 0 {
		return "", fmt.Errorf("本会话的定义没有列出可委派的智能体（[use.subagent] agents），agent 只能留空")
	}
	return "", fmt.Errorf("智能体 %s 不在可委派列表里（%s）", name, strings.Join(u.Agents, "、"))
}
