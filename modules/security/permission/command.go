package permission

// 命令拆分：allow 规则逐段匹配复合命令；“本次会话内都允许”按命令前缀生成规则（Claude Code 写法 `git push:*`）。

import (
	"encoding/json"

	"github.com/Sevyn17/tenon/modules/internal/cmdline"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// 命令拆分与安全段判断与 shell 插件共用一份（modules/internal/cmdline，审查 P1-61）。
var (
	SplitCommand = cmdline.SplitCommand
	Safe         = cmdline.Safe
	Prefix       = cmdline.Prefix
)

// SessionAllow “本次会话内都允许”对应的规则。
// shell：复合命令里每个非安全简单命令生成一条前缀规则（`git push:*`）；无法拆分的命令不生成规则（只批准本次）；
// 申请了更宽沙箱时规则带上该沙箱模式（之后同类命令的同等申请免确认）。其余工具为整个工具。
func SessionAllow(tool string, args json.RawMessage) []Rule {
	subj, cmd := Subject(tool, args, "")
	if !cmd {
		return []Rule{{Tool: tool, Action: Allow}}
	}
	sb, _ := toolhub.RequestedSandbox(args)
	segs, ok := SplitCommand(subj)
	if !ok {
		return nil
	}
	var out []Rule
	seen := map[string]bool{}
	add := func(seg string) {
		p := Prefix(seg) + ":*"
		if !seen[p] {
			seen[p] = true
			out = append(out, Rule{Tool: tool, Pattern: p, Action: Allow, Sandbox: sb})
		}
	}
	for _, s := range segs {
		// 启动器停止线：sh / python / npm run 这类执行的是别处的程序，前缀规则会放行任意内容，
		// 只批准本次、不生成会话规则（约束清单「shell 本次会话内都允许」；审查 P1-61：原先这里没判断，停止线从未生效）。
		if cmdline.Launcher(s) {
			return nil
		}
		if !Safe(s) {
			add(s)
		}
	}
	if len(out) == 0 {
		add(segs[0])
	}
	return out
}

// allowed allow 规则判定。命令：拆成简单命令，每个非安全命令都要命中某条 allow 规则（ok 额外筛选规则）；
// 无法拆分的命令只认无模式规则（整工具）。其余：任一规则命中。
func (p Policy) allowed(name string, args json.RawMessage, ok func(Rule) bool) (Rule, bool) {
	cands := []Rule{}
	for _, r := range p.Rules {
		if r.Action == Allow && (r.Tool == "*" || r.Tool == name) && ok(r) {
			cands = append(cands, r)
		}
	}
	if len(cands) == 0 {
		return Rule{}, false
	}
	subj, cmd := Subject(name, args, p.Root)
	if !cmd {
		for _, r := range cands {
			if r.Pattern == "" || Glob(r.Pattern, subj) {
				return r, true
			}
		}
		return Rule{}, false
	}
	segs, splittable := SplitCommand(subj)
	if !splittable {
		for _, r := range cands {
			if r.Pattern == "" {
				return r, true
			}
		}
		return Rule{}, false
	}
	var first Rule
	found := false
	for _, s := range segs {
		hit := false
		for _, r := range cands {
			if r.Pattern == "" || Glob(r.Pattern, s) {
				hit = true
				if !found {
					first, found = r, true
				}
				break
			}
		}
		if !hit && !Safe(s) {
			return Rule{}, false
		}
	}
	if !found { // 全是安全命令：仍需有规则才算规则放行
		for _, r := range cands {
			if r.Pattern == "" {
				return r, true
			}
		}
		return Rule{}, false
	}
	return first, true
}

// AllowsSandbox 本次调用申请的更宽沙箱 req 是否已被 allow 规则（带不低于 req 的沙箱模式）覆盖。
func (p Policy) AllowsSandbox(name string, args json.RawMessage, req toolhub.SandboxMode) bool {
	_, ok := p.allowed(name, args, func(r Rule) bool {
		return r.Sandbox != "" && !toolhub.SandboxWider(req, r.Sandbox)
	})
	return ok
}
