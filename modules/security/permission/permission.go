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
	"runtime"
	"strings"
	"time"

	"ybase/runtime/toolhub"
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
	// Cap 定义给的权限上限（[use.permission]，08 8.3）；非空时另按它判一次，取两者更严的结论。
	Cap *Policy
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

// Decide 纯函数判定（便于测试与 explain）。有上限档位（Cap）时两边各判一次，取更严的结论：
// 上限只能收窄，会话档位再宽也越不过定义给的上限。
func (p Policy) Decide(cx *toolhub.Cx, args json.RawMessage) Decision {
	d := p.decide(cx, args)
	if p.Cap == nil {
		return d
	}
	c := *p.Cap
	c.Cap, c.Untrusted = nil, p.Untrusted
	cd := c.decide(cx, args)
	if severity(cd.Action) > severity(d.Action) {
		cd.Reason = "智能体权限上限 " + string(c.Mode) + "：" + cd.Reason
		return cd
	}
	return d
}

func (p Policy) decide(cx *toolhub.Cx, args json.RawMessage) Decision {
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
	if r, ok := q.unseen(name, args, rules); ok {
		return Decision{Action: AskUser, Reason: fmt.Sprintf("命令含规则看不全的写法（命令替换 / 后台 / 重定向），规则 %s 无法确认未命中，改为询问", r)}
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
	if !cmd {
		return Glob(r.Pattern, subj)
	}
	// deny / ask：复合命令任一段命中即命中；空白压成一个（`rm  -rf` 与 `rm -rf` 同一条），
	// 启动器包着的命令（`cmd /c …`、`bash -c "…"`）拆开再看一遍里面的命令。
	for _, seg := range commandViews(subj, 0) {
		if Glob(r.Pattern, seg) {
			return true
		}
	}
	return false
}

// unseen deny / ask 规则看不全的命令：拆不开（命令替换、后台 &、写文件重定向、脚本块），
// 规则只能对整条命令做前缀匹配，里面藏的命令看不到。有针对命令的 deny / ask 规则时，
// 这类命令至少要询问——否则公共 deny 换个写法就绕过去了。
func (p Policy) unseen(name string, args json.RawMessage, rules []Rule) (Rule, bool) {
	subj, cmd := Subject(name, args, p.Root)
	if !cmd {
		return Rule{}, false
	}
	if _, ok := SplitCommand(normSpace(subj)); ok {
		return Rule{}, false
	}
	for _, r := range rules {
		if (r.Action == Deny || r.Action == AskUser) && r.Pattern != "" && (r.Tool == "*" || r.Tool == name) {
			return r, true
		}
	}
	return Rule{}, false
}

// commandViews 一条命令供规则匹配的各个视图：压过空白的整条、拆开的每一段，
// 以及启动器（cmd /c、sh/bash -c、pwsh -Command）包着的内层命令（递归，最多 3 层）。
func commandViews(c string, depth int) []string {
	c = normSpace(c)
	views := []string{c}
	segs, ok := SplitCommand(c)
	if !ok {
		segs = nil
	}
	for _, s := range segs {
		if s != c {
			views = append(views, s)
		}
	}
	if depth >= 3 {
		return views
	}
	for _, s := range append([]string{c}, segs...) {
		if inner, ok := launcherPayload(s); ok {
			views = append(views, commandViews(inner, depth+1)...)
		}
	}
	return views
}

// normSpace 把连续空白压成一个空格并去掉首尾空白。
func normSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

// launcherPayload 取出 `cmd /c X`、`sh -c "X"`、`bash -lc 'X'`、`pwsh -Command X` 里的 X。
func launcherPayload(seg string) (string, bool) {
	f := strings.Fields(seg)
	if len(f) < 3 {
		return "", false
	}
	exe := strings.ToLower(filepath.Base(strings.Trim(f[0], `"'`)))
	exe = strings.TrimSuffix(strings.TrimSuffix(exe, ".exe"), ".cmd")
	flag := strings.ToLower(f[1])
	var ok bool
	switch exe {
	case "cmd":
		ok = flag == "/c" || flag == "/k"
	case "sh", "bash", "zsh", "dash":
		ok = strings.HasPrefix(flag, "-") && strings.HasSuffix(flag, "c")
	case "powershell", "pwsh":
		ok = flag == "-command" || flag == "-c"
	}
	if !ok {
		return "", false
	}
	rest := strings.TrimSpace(seg[strings.Index(seg, f[1])+len(f[1]):])
	if len(rest) >= 2 && (rest[0] == '"' || rest[0] == '\'') && rest[len(rest)-1] == rest[0] {
		rest = rest[1 : len(rest)-1]
	}
	return rest, rest != ""
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

// foldCase 规则匹配是否忽略大小写：Windows 文件系统与 PowerShell 命令名都不分大小写，
// 区分大小写时 write_file(.env) 拦不住写 .ENV（2026-11-04 审查 P1-13）。测试可改。
var foldCase = runtime.GOOS == "windows"

// Glob `*` 匹配任意字符（含 /）；结尾 `:*` 等同 `*`（前缀）。Windows 上不分大小写，其余平台区分。
func Glob(pat, s string) bool {
	if foldCase {
		pat, s = strings.ToLower(pat), strings.ToLower(s)
	}
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

// Layer 构造 toolhub Guard 层。policy 按会话取（档位可切换）。已批准的调用（cx.Approved，toolhub 按挂起记录
// 核对过参数哈希）免确认。
func Layer(policy func(sessionID string) Policy) toolhub.Layer {
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
				return toolhub.Failf("权限拒绝：%s", d.Reason), nil
			default:
				// 拒绝类在上面已判；这里只有确认类，批准记录可免确认（参数必须一致）。
				if cx.Approved {
					return next(ctx, cx, args)
				}
				return toolhub.Output{Kind: toolhub.Confirm, Reason: d.Reason}, nil
			}
		},
	}
}
