// Package sessions 提供 session_start（Mortise docs/70、docs/73 S1）：给某个智能体新建会话并交给它第一条消息。
// 三种用法（docs/76 C2）：child + wait 同步跑子会话一个回合并返回最终回答；child + notify|none 派出子会话不等它；
// independent 建顶层独立会话交给对方负责（缺省 notify，结果以 notice 回投创建者）。并发、深度两层、联系策略都在这里先判一次。
// 子会话 origin 字面量仍是 "subagent"（宿主据此判断子会话，docs/73 R1），内置临时智能体定义名仍是 "subagent"。
package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Sevyn17/tenon/runtime/host"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
	"github.com/Sevyn17/tenon/runtime/tasks"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// 名字。
const (
	PluginName = "session-start" // 组件名（profile 键、[use.session-start]）
	ToolName   = "session_start" // 模型工具名
	// Origin 子会话行的 origin（不改，宿主按它判断子会话）。
	Origin = "subagent"
)

// relation / reply 取值。
const (
	RelationChild       = "child"
	RelationIndependent = "independent"
	ReplyWait           = "wait"
	ReplyNotify         = "notify"
	ReplyNone           = "none"
)

// normalize 规范化 relation / reply（docs/76 C2）：缺省 child + wait；independent 缺省 notify；
// independent + wait 不合法（同步等待只对子会话有意义）。返回规范值，或给模型看的错误说明。
func normalize(relation, reply string) (string, string, string) {
	switch relation {
	case "":
		relation = RelationChild
	case RelationChild, RelationIndependent:
	default:
		return "", "", fmt.Sprintf("relation 只能是 %s 或 %s，得到 %q", RelationChild, RelationIndependent, relation)
	}
	switch reply {
	case "":
		reply = ReplyWait
		if relation == RelationIndependent {
			reply = ReplyNotify
		}
	case ReplyWait, ReplyNotify, ReplyNone:
	default:
		return "", "", fmt.Sprintf("reply 只能是 %s、%s 或 %s，得到 %q", ReplyWait, ReplyNotify, ReplyNone, reply)
	}
	if relation == RelationIndependent && reply == ReplyWait {
		return "", "", "relation=independent 不能配 reply=wait：独立会话不等结果；要等结果用 child，要结果回投用 notify"
	}
	return relation, reply, ""
}

// 并发上限（docs/76 §5.1 D12、已定 Q1）：[plugin.session-start] max_concurrent，缺省 4，范围 1–8，硬上限写死。
const (
	DefaultMaxConcurrent = 4
	MaxConcurrentCap     = 8
)

// clampConcurrent 把配置值夹到 1..MaxConcurrentCap；0 = 缺省。
func clampConcurrent(n int) int {
	if n <= 0 {
		return DefaultMaxConcurrent
	}
	if n > MaxConcurrentCap {
		return MaxConcurrentCap
	}
	return n
}

// SubagentTimeout 委派工具默认超时（子回合可能很长）。
const SubagentTimeout = 30 * time.Minute

// Params 委派工具开放哪些按次参数（Mortise docs/62 K5、docs/56 第 2 节）：开着的才出现在工具 schema 里；
// 关着的取缺省行为（只用基础提示词 / 跟随主会话模型 / 继承主会话档位 / 继承主会话工具集）。task 必有，不在这里。
type Params struct {
	SystemPrompt bool
	Model        bool
	Mode         bool
	Tools        bool
	// MaxConcurrent 本会话同时在跑的派出会话上限（[plugin.session-start] max_concurrent）；0 = 缺省 4，夹到 1..8。
	MaxConcurrent int
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
		"agent":   map[string]any{"type": "string", "description": "可选：执行的智能体定义名；只能选本会话定义 [use.session-start] agents 里列出的；缺省为临时智能体"},
		"relation": map[string]any{"type": "string", "enum": []string{RelationChild, RelationIndependent},
			"description": "新会话与本会话的关系：child（缺省，子会话，归本会话管、随本会话删除）；independent（独立会话，交给对方负责，不随本会话删除）"},
		"reply": map[string]any{"type": "string", "enum": []string{ReplyWait, ReplyNotify, ReplyNone},
			"description": "结果怎么回来：wait（child 缺省，等它做完并返回最终回答）；notify（independent 缺省，派出后立即返回，对方做完时结果作为通知回到本会话）；none（不回报）"},
		"wake": map[string]any{"type": "boolean", "description": "可选，只对 notify：对方做完时立即唤醒本会话开一轮处理结果；缺省不唤醒，结果在下次对话时作为背景出现"},
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
	return ToolWithContacts(h, f, sink, p, nil)
}

// ToolWithContacts 同 ToolWithParams，另带联系策略（docs/76 §6；nil = 只看定义名单）。
func ToolWithContacts(h *host.Host, f host.Factory, sink tasks.Sink, p Params, contacts ContactPolicy) *toolhub.Tool {
	// 一批里的多个委派并行跑（2026-11-04 审查 P2 工具体验：原先串行）；续用同一子会话的调用按 id 串行，见 childLocks。
	return ToolWithOptions(h, f, sink, p, contacts, DefaultMaxConcurrent)
}

// ToolWithOptions 同 ToolWithContacts，另带并发上限 max（[plugin.session-start] max_concurrent，夹到 1..8）。
func ToolWithOptions(h *host.Host, f host.Factory, sink tasks.Sink, p Params, contacts ContactPolicy, max int) *toolhub.Tool {
	max = clampConcurrent(max)
	yes := true
	var locks childLocks
	slots := &concurrency{}
	return &toolhub.Tool{
		Spec: session.ToolSpec{Name: ToolName,
			Description: "把一件事交给另一个智能体做，在新会话里完成（全新上下文）。三种用法：child + wait（缺省）等它做完、返回最终回答；" +
				"child + notify 派出去自己接着干，做完结果作为通知回到本会话；independent 交给对方负责（独立会话，缺省 notify）。" +
				"一两步就能做完的事自己做，不要派；同一件事不要派多个。task 要自包含：对方看不到当前对话。" +
				"wait 时传 session 可在同一个子会话里追问。",
			Schema: schemaFor(p)},
		Risk:     toolhub.Write,
		Parallel: &yes,
		Timeout:  SubagentTimeout,
		Items:    []string{ItemSpawn},
		Call: func(ctx context.Context, cx *toolhub.Cx, raw json.RawMessage) (toolhub.Output, error) {
			var a struct {
				Task, Session, Agent string
				Relation, Reply      string
				SystemPrompt         string `json:"system_prompt"`
				Model, Mode          string
				Tools                []string
			}
			if err := json.Unmarshal(raw, &a); err != nil || strings.TrimSpace(a.Task) == "" {
				return toolhub.Failf("task 不能为空"), nil
			}
			relation, reply, msg := normalize(strings.TrimSpace(a.Relation), strings.TrimSpace(a.Reply))
			if msg != "" {
				return toolhub.Failf("%s", msg), nil
			}
			// 创建链两层（docs/76 C1）：Visible 已隐藏，这里按会话身份再校验一次，防定义白名单或旧会话绕过。
			if cx.Ctx != nil {
				if d := cx.Ctx.Session().ChainDepth; d >= store.MaxChainDepth {
					return toolhub.Failf("本会话已在创建链第 %d 层，不能再建会话（最多 %d 层）；需要协作时把结果交回创建你的会话", d, store.MaxChainDepth), nil
				}
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
			if cx.Ctx != nil {
				if err := checkContact(ctx, contacts, cx.Ctx.Session().Definition, def); err != nil {
					return toolhub.Failf("%v", err), nil
				}
			}
			// 并发上限（docs/76 §5.1 D12）：按派出方加锁，先登记名额再建会话，失败回滚登记。
			release, cerr := slots.acquire(cx.SessionID, max)
			if cerr != nil {
				return toolhub.Failf("%v", cerr), nil
			}
			if relation == RelationIndependent || reply != ReplyWait {
				if a.Session != "" {
					release()
					return toolhub.Failf("续用已有会话请等结果（reply=wait）；异步派出总是新建会话"), nil
				}
				so := host.StartOptions{Creator: cx.SessionID, Definition: def, Child: relation == RelationChild,
					Task: a.Task, ReplyMode: reply}
				if so.Child {
					so.ID, so.Spawn, so.Factory = id, o, f
				}
				if hd, ok := h.Get(cx.SessionID); ok {
					so.Stamp = hd.Agent().Stamp
				}
				nid, err := h.StartSession(ctx, so)
				if err != nil {
					release()
					return toolhub.Failf("派出会话失败：%v", err), nil
				}
				track(h, nid, release)
				cx.ChildSessionID = nid
				how := "做完后结果会作为通知回到本会话（不可信背景，不开新回合）"
				if reply == ReplyNone {
					how = "它做完不会回投，需要时自己去看"
				}
				kind := "独立会话"
				if so.Child {
					kind = "子会话"
				}
				return toolhub.Text(fmt.Sprintf("[已派出%s %s，正在后台运行；%s]", kind, nid, how)), nil
			}
			defer release()
			cx.ChildSessionID = id
			unlock := locks.lock(id)
			defer unlock()
			t := tasks.Task{ID: id, Session: cx.SessionID, Kind: tasks.KindSession, Title: taskTitle(a.Task),
				Status: tasks.Running, Started: time.Now()}
			if sink != nil {
				sink.Started(t)
			}
			out, _, err := h.SpawnWith(ctx, cx.SessionID, id, Origin, def, f, a.Task, h.Untrusted(cx.SessionID), o)
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

// UseConfig 定义里的 [use.session-start]：本智能体派子智能体时可选的定义（2026-11-04 审查 P2：原先只能用内置的通用定义）。
// 子会话仍不越过父会话（父定义的 tools 白名单与 [use.permission] 上限都叠加到子会话上，见 assemble.ParentLimits）。
//
//	[use.session-start]
//	agents = ["reviewer", "researcher"]
type UseConfig struct {
	Agents []string `toml:"agents" limit:"intersect" description:"subagent 工具的 agent 参数可选的定义名；不写 = 只能用通用子智能体"`
}

// pickDefinition 本次委派用的定义：没给 = 内置通用定义；给了必须在 [use.session-start] agents 里。
func pickDefinition(cx *toolhub.Cx, name string) (string, error) {
	if name == "" || name == Definition {
		return Definition, nil
	}
	var u UseConfig
	if cx.Ctx != nil {
		if err := cx.Ctx.Config(PluginName, &u); err != nil {
			return "", err
		}
	}
	for _, a := range u.Agents {
		if a == name {
			return name, nil
		}
	}
	if len(u.Agents) == 0 {
		return "", fmt.Errorf("本会话的定义没有列出可委派的智能体（[use.session-start] agents），agent 只能留空")
	}
	return "", fmt.Errorf("智能体 %s 不在可委派列表里（%s）", name, strings.Join(u.Agents, "、"))
}
