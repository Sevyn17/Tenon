// Package permission：权限 Guard（docs/90 - 归档/20 - 插件/05 - 权限.md，docs/90 - 归档/30 - 计划与报告/06 - 工具权限设计方案.md，安全项）。
//
// 档位就是配置文件（modes.go）：内置 read-only / ask / workspace（缺省）/ full，另可自定义。
// 判定顺序：deny 规则 → ask 规则 → allow 规则（公共 + 档位 + 本会话）→ 档位 [tools] → 档位 [effects] → 询问。
// 之后只收紧：申请比本档更宽的沙箱要确认（带 @沙箱 的 allow 规则可覆盖）；不可信输入回合 allow 降级为 ask；
// MCP 声明 destructiveHint 的工具 allow 降级为 ask。
package permission

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"ybase-kernel/core/toolhub"
)

// Mode 档位名（档位文件名，不含 .toml）。
type Mode string

// 内置档位。
const (
	ReadOnly  Mode = "read-only"
	Ask       Mode = "ask"
	Workspace Mode = "workspace"
	Full      Mode = "full"
)

// Default 新会话缺省档位。
const Default = Workspace

// Builtins 内置档位（展示顺序）。
var Builtins = []Mode{ReadOnly, Ask, Workspace, Full}

// Action 规则动作。
type Action string

const (
	Allow   Action = "allow"
	AskUser Action = "ask"
	Deny    Action = "deny"
)

// AttrWorkspace 工具属性：本工具只能改工作区内的文件（路径已在工具内限定）。
const AttrWorkspace = toolhub.AttrWorkspace

// AttrDestructive 工具属性：服务端声明本工具有破坏性（MCP destructiveHint），allow 降级为 ask，且不提供“本会话允许”。
const AttrDestructive = "destructive"

// Decision 判定结果。
type Decision struct {
	Action Action
	Reason string
}

// Policy 一个会话的权限策略。
type Policy struct {
	Mode Mode
	Def  *ModeDef // 档位定义；nil = 内置缺省档位
	// Rules 公共规则 + 本会话规则（档位 [rules] 在 Def 里）。
	Rules []Rule
	// Root 工作区根（路径规则相对它匹配）。
	Root string
	// Sandbox 固定沙箱（命令行 -sandbox）；空 = 按档位 [sandbox]。
	Sandbox toolhub.SandboxMode
	// Untrusted 报告本次调用所在回合的输入是否不可信（可空）。
	Untrusted func(cx *toolhub.Cx) bool
}

func (p Policy) def() *ModeDef {
	if p.Def != nil {
		return p.Def
	}
	if p.Mode != "" {
		if d := builtinDef(p.Mode); d != nil {
			return d
		}
	}
	return builtinDef(Default)
}

// SandboxOf 工具在本策略下的沙箱模式。
func (p Policy) SandboxOf(tool string) toolhub.SandboxMode {
	if p.Sandbox != "" {
		return p.Sandbox
	}
	_ = tool
	return p.def().SandboxLevel()
}

func (p Policy) allRules() []Rule {
	return append([]Rule(nil), p.Rules...)
}

// Decide 纯函数判定（便于测试与 explain）。
func (p Policy) Decide(cx *toolhub.Cx, args json.RawMessage) Decision {
	name := cx.Tool.Spec.Name
	d := p.base(cx, args)
	if d.Action != Allow {
		return d
	}
	if on, _ := cx.Tool.Attrs[AttrDestructive].(bool); on {
		return Decision{Action: AskUser, Reason: "服务端声明此操作具有破坏性"}
	}
	if p.Untrusted != nil && p.Untrusted(cx) {
		return Decision{Action: AskUser, Reason: d.Reason + "（输入不可信，降级为确认）"}
	}
	_ = name
	return d
}

func (p Policy) base(cx *toolhub.Cx, args json.RawMessage) Decision {
	name := cx.Tool.Spec.Name
	rules := p.allRules()
	q := p
	q.Rules = rules
	for _, act := range []Action{Deny, AskUser} {
		for _, r := range rules {
			if r.Action == act && q.hit(r, name, args) {
				return Decision{Action: act, Reason: fmt.Sprintf("规则 %s → %s", r, act)}
			}
		}
	}
	// 申请了比本档更宽的沙箱：只认带足够沙箱模式的 allow 规则，否则询问
	req, _ := toolhub.RequestedSandbox(args)
	sb := p.SandboxOf(name)
	wider := false
	if on, _ := cx.Tool.Attrs[toolhub.AttrSandboxed].(bool); on {
		wider = req != "" && toolhub.SandboxWider(req, sb)
	}
	if r, ok := q.allowed(name, args, func(r Rule) bool {
		return !wider || (r.Sandbox != "" && !toolhub.SandboxWider(req, r.Sandbox))
	}); ok {
		return Decision{Action: Allow, Reason: fmt.Sprintf("规则 %s → allow", r)}
	}
	def := p.def()
	act, why := AskUser, ""
	if cx.Tool.NoAuth {
		act, why = Allow, fmt.Sprintf("%s 声明无需授权", name)
	} else if dec, ok := evalItems(def, cx.Items, name, args); ok {
		act, why = dec.Action, dec.Reason
	} else if len(cx.Tool.Items) > 0 {
		why = fmt.Sprintf("%s 的权限项只有级别项，按询问处理", name)
	} else {
		// §9.3：未声明权限项的工具一律询问（漏声明的代价是啰嗦，不是失守）。
		why = fmt.Sprintf("%s 未声明权限项，按询问处理", name)
	}
	if act == Allow && wider {
		return Decision{Action: AskUser, Reason: fmt.Sprintf("%s 申请沙箱 %s（本档 %s）", name, req, sb)}
	}
	return Decision{Action: act, Reason: why}
}

// effectLabel 效果的中文名。
func effectLabel(e string) string {
	switch e {
	case toolhub.EffectRead:
		return "读"
	case toolhub.EffectWrite:
		return "写工作区"
	case toolhub.EffectExec:
		return "执行"
	case toolhub.EffectNetwork:
		return "联网"
	case toolhub.EffectInteract:
		return "交互"
	case toolhub.EffectSpawn:
		return "派生"
	case toolhub.EffectCollab:
		return "协作写"
	case toolhub.EffectOrchestrate:
		return "编排"
	case toolhub.EffectExternal:
		return "外来（MCP）"
	}
	return e
}

// Rule 一条规则。Tool 为工具名（"*" 匹配全部）。
// Pattern 匹配调用的“对象”：shell 为命令，带 path 的工具为相对工作区的路径，其余为参数 JSON；
// `*` 匹配任意字符，结尾 `:*` 表示前缀（Claude Code 写法）。空 = 任意。
type Rule struct {
	Tool    string `toml:"tool"`
	Pattern string `toml:"pattern"`
	Action  Action `toml:"action"`
	// Sandbox 非空：本规则同时批准申请不高于该模式的更宽沙箱（写法 `工具(模式)@沙箱模式`）。
	Sandbox toolhub.SandboxMode `toml:"sandbox"`
}

// String 规则的 `工具(模式)` 写法。
func (r Rule) String() string {
	out := r.Tool
	if r.Pattern != "" {
		out += "(" + r.Pattern + ")"
	}
	if r.Sandbox != "" {
		out += "@" + string(r.Sandbox)
	}
	return out
}

// ParseRule 解析 `工具` 或 `工具(模式)`。
func ParseRule(s string, a Action) (Rule, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Rule{}, fmt.Errorf("permission: 空规则")
	}
	var sb toolhub.SandboxMode
	if j := strings.LastIndexByte(s, '@'); j > 0 && !strings.ContainsAny(s[j:], ")]") {
		m, err := toolhub.ParseSandboxMode(s[j+1:])
		if err != nil {
			return Rule{}, fmt.Errorf("permission: 规则 %q 沙箱模式不合法", s)
		}
		sb, s = m, strings.TrimSpace(s[:j])
	}
	i := strings.IndexByte(s, '(')
	if i < 0 {
		if strings.ContainsAny(s, "[]()") {
			return Rule{}, fmt.Errorf("permission: 规则 %q 格式应为 工具 或 工具(模式)", s)
		}
		return Rule{Tool: s, Action: a, Sandbox: sb}, nil
	}
	if !strings.HasSuffix(s, ")") || i == 0 {
		return Rule{}, fmt.Errorf("permission: 规则 %q 格式应为 工具(模式)", s)
	}
	return Rule{Tool: strings.TrimSpace(s[:i]), Pattern: s[i+1 : len(s)-1], Action: a, Sandbox: sb}, nil
}

func (p Policy) hit(r Rule, name string, args json.RawMessage) bool {
	if r.Tool != "*" && r.Tool != name {
		return false
	}
	if r.Pattern == "" {
		return true
	}
	subj, cmd := Subject(name, args, p.Root)
	if cmd { // deny / ask：复合命令任一段命中即命中
		if segs, ok := SplitCommand(subj); ok {
			for _, seg := range segs {
				if Glob(r.Pattern, seg) {
					return true
				}
			}
		}
	}
	return Glob(r.Pattern, subj)
}

// Subject 调用的匹配对象：shell 命令（cmd=true）/ 相对路径 / 参数 JSON。
func Subject(tool string, args json.RawMessage, root string) (s string, cmd bool) {
	var a struct {
		Command string `json:"command"`
		Path    string `json:"path"`
	}
	_ = json.Unmarshal(args, &a)
	switch {
	case a.Command != "":
		return strings.TrimSpace(a.Command), true
	case a.Path != "":
		p := a.Path
		if root != "" && filepath.IsAbs(p) {
			if rel, err := filepath.Rel(root, p); err == nil {
				p = rel
			}
		}
		return filepath.ToSlash(filepath.Clean(p)), false
	}
	return string(args), false
}

// Glob `*` 匹配任意字符（含 /）；结尾 `:*` 等同 `*`（前缀）。大小写敏感。
func Glob(pat, s string) bool {
	if strings.HasSuffix(pat, ":*") {
		pat = strings.TrimSuffix(pat, ":*") + "*"
	}
	pat = strings.ReplaceAll(pat, "**", "*")
	parts := strings.Split(pat, "*")
	if len(parts) == 1 {
		return pat == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	last := parts[len(parts)-1]
	for _, mid := range parts[1 : len(parts)-1] {
		i := strings.Index(s, mid)
		if i < 0 {
			return false
		}
		s = s[i+len(mid):]
	}
	return strings.HasSuffix(s, last)
}

// Approved 报告某调用是否已获人工批准（参数哈希锁定）。由宿主按挂起记录实现。
type Approved func(sessionID, callID, argsHash string) bool

// Layer 构造 toolhub Guard 层。policy 按会话取（档位可切换）；approved 可空（未接挂起时）。
func Layer(policy func(sessionID string) Policy, approved Approved) toolhub.Layer {
	return toolhub.Layer{
		Name:       "permission",
		Guard:      true,
		Authorizes: true,
		Timeout:    5 * time.Second,
		Call: func(ctx context.Context, cx *toolhub.Cx, args json.RawMessage, next toolhub.Next) (toolhub.Output, error) {
			d := policy(cx.SessionID).Decide(cx, args)
			switch d.Action {
			case Allow:
				return next(ctx, cx, args)
			case Deny:
				return toolhub.Errorf("权限拒绝：%s", d.Reason), nil
			default:
				// 拒绝类在上面已判；这里只有确认类，批准记录可免确认（参数必须一致）。
				if cx.Approved || (approved != nil && approved(cx.SessionID, cx.CallID, toolhub.ArgsHash(args))) {
					return next(ctx, cx, args)
				}
				return toolhub.Output{Kind: toolhub.Confirm, Reason: d.Reason}, nil
			}
		},
	}
}
