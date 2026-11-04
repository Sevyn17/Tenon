package permission

// 命令拆分：allow 规则逐段匹配复合命令；“本次会话内都允许”按命令前缀生成规则（Claude Code 写法 `git push:*`）。

import (
	"encoding/json"
	"strings"

	"ybase/runtime/toolhub"
)

// safeHeads 无副作用、不需要规则即可出现在复合命令里的命令（不区分大小写）。
var safeHeads = map[string]bool{
	"cd": true, "set-location": true, "pwd": true, "get-location": true,
	"echo": true, "write-output": true, "write-host": true, "true": true,
	"select-object": true, "out-string": true, "out-null": true,
	"format-table": true, "format-list": true, "measure-object": true,
	"head": true, "tail": true, "wc": true,
}

// harmless 可直接去掉的重定向（只合并 / 丢弃输出，不写文件）。
var harmless = []string{"2>&1", "2>$null", ">$null", "2>/dev/null", ">/dev/null", "*>$null"}

// SplitCommand 把命令按 ; && || | 换行拆成简单命令。含命令替换、脚本块、后台 &、写文件重定向时 ok=false。
func SplitCommand(c string) (segs []string, ok bool) {
	var cur strings.Builder
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			segs = append(segs, s)
		}
		cur.Reset()
	}
	var quote byte
	for i := 0; i < len(c); i++ {
		ch := c[i]
		if quote != 0 {
			if ch == quote {
				quote = 0
			} else if quote == '"' && (ch == '`' || (ch == '$' && i+1 < len(c) && c[i+1] == '(')) {
				return nil, false // 双引号内的命令替换 / 转义
			}
			cur.WriteByte(ch)
			continue
		}
		rest := c[i:]
		if h := harmlessAt(rest); h != "" {
			i += len(h) - 1
			continue
		}
		switch ch {
		case '\'', '"':
			quote = ch
			cur.WriteByte(ch)
		case ';', '\n', '\r':
			flush()
		case '|', '&':
			if i+1 < len(c) && c[i+1] == ch { // && ||
				i++
				flush()
			} else if ch == '|' {
				flush()
			} else {
				return nil, false // 单个 &：后台（bash）/ 调用运算符（PowerShell）
			}
		case '`', '{', '}', '<', '>':
			return nil, false
		case '$':
			if i+1 < len(c) && c[i+1] == '(' {
				return nil, false
			}
			cur.WriteByte(ch)
		default:
			cur.WriteByte(ch)
		}
	}
	if quote != 0 {
		return nil, false
	}
	flush()
	return segs, len(segs) > 0
}

func harmlessAt(s string) string {
	for _, h := range harmless {
		if strings.HasPrefix(s, h) {
			return h
		}
	}
	return ""
}

func head(seg string) string {
	f := strings.Fields(seg)
	if len(f) == 0 {
		return ""
	}
	return strings.ToLower(f[0])
}

// Safe 简单命令是否无需规则。
func Safe(seg string) bool { return safeHeads[head(seg)] }

// Prefix 简单命令的规则前缀：首词，第二词是子命令（不以 - / 引号开头、不像路径）时带上。
func Prefix(seg string) string {
	f := strings.Fields(seg)
	if len(f) == 0 {
		return ""
	}
	n := 1
	if len(f) > 1 {
		w := f[1]
		if !strings.ContainsAny(w[:1], "-\"'$@") && !strings.ContainsAny(w, `/\.:=*`) {
			n = 2
		}
	}
	return strings.Join(f[:n], " ")
}

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
