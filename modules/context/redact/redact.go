// Package redact 脱敏：把工具输出里的密钥替换成占位符，再进入日志、产物与模型上下文。
//
// 两类规则：
//   - 已知值：进程环境里名字像密钥的变量（KEY/TOKEN/SECRET/PASSWORD…）的值，精确替换；
//   - 模式：常见密钥形状（参考 Codex secrets/sanitizer.rs）。
//
// 尽力而为，不是安全边界：防的是密钥被顺手写进会话库和发给模型。
package redact

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"ybase/runtime/session"
	"ybase/runtime/toolhub"
)

// Placeholder 替换文本。
const Placeholder = "[REDACTED_SECRET]"

// MinValueLen 已知值的最短长度（太短的值精确替换会误伤）。
const MinValueLen = 8

var (
	reBearer = regexp.MustCompile(`(?i:\bBearer)[ \t]+[A-Za-z0-9._~+/-]{16,}=*`)
	rePrefix = regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]{20,}|AKIA[0-9A-Z]{16}|gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{40,}|xox[baprs]-[A-Za-z0-9-]{10,}|AIza[0-9A-Za-z_-]{35})`)
	// reAssign 键 = 值。键名可被引号包住（JSON / YAML 的 "api_key": "…"），带引号的值允许空格
	// （2026-11-04 审查 P1-11：原先这两种最常见的配置文件写法都漏掉）。分组：1 键名、2 分隔（含键的右引号）、
	// 3 双引号值、4 单引号值、5 不带引号的值。
	reAssign = regexp.MustCompile(`(?i)\b([a-z0-9_]*(?:api[_-]?key|token|secret|password|passwd)[a-z0-9_]*)\b(["']?\s*[:=]\s*)(?:"([^"\n]{8,})"|'([^'\n]{8,})'|([^\s"']{8,}))`)
	// reNotSecretName 名字像密钥、但按惯例装的是路径的变量（KEYCHAIN_PATH、SSH_KEY_DIR、*_KEYFILE）。
	reNotSecretName = regexp.MustCompile(`(?i)(_PATH|_DIR|_FILE|FILE)$`)
	// reIdent 不带引号的值若只是标识符 / 成员访问（createToken、process.env.TOKEN），是代码不是密钥。
	reIdent = regexp.MustCompile(`^[A-Za-z_$][A-Za-z_$]*(?:\.[A-Za-z_$][A-Za-z0-9_$]*)*$`)
	// reURL 连接串里的口令：scheme://user:password@host（保留用户名）。
	reURL  = regexp.MustCompile(`\b([a-zA-Z][a-zA-Z0-9+.-]*://[^\s:/@]*:)[^\s@/]+@`)
	rePEM  = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`)
	reName = regexp.MustCompile(`(?i)(KEY|TOKEN|SECRET|PASSWORD|PASSWD|CREDENTIAL)`)
)

// Redactor 一组已知值 + 模式。
type Redactor struct {
	values []string // 长的在前
}

// New 用给定已知值构造。
func New(values ...string) *Redactor {
	r := &Redactor{}
	seen := map[string]bool{}
	for _, v := range values {
		v = strings.TrimSpace(v)
		if len(v) >= MinValueLen && !seen[v] {
			seen[v] = true
			r.values = append(r.values, v)
		}
	}
	sort.Slice(r.values, func(i, j int) bool { return len(r.values[i]) > len(r.values[j]) })
	return r
}

// FromEnv 收集环境变量中名字像密钥的值。名字按惯例是路径的变量、值像路径的不收：
// 否则工作目录这类路径在所有输出里都变成占位符，WriteGuard 再拒写（2026-11-04 审查 P1-12）。
func FromEnv(environ []string) *Redactor {
	var vals []string
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if ok && reName.MatchString(k) && !reNotSecretName.MatchString(k) && !looksLikePath(v) {
			vals = append(vals, v)
		}
	}
	return New(vals...)
}

// looksLikePath 值像文件路径：绝对路径（/、~、\\、盘符）或 ./ ../ 开头。只看开头：
// 密钥里也可能带 /（如 base64），但不会以这些开头。
func looksLikePath(v string) bool {
	v = strings.TrimSpace(v)
	switch {
	case v == "":
		return false
	case strings.HasPrefix(v, "/"), strings.HasPrefix(v, "~"), strings.HasPrefix(v, `\`),
		strings.HasPrefix(v, "./"), strings.HasPrefix(v, "../"), strings.HasPrefix(v, `.\`):
		return true
	}
	return len(v) >= 3 && v[1] == ':' && (v[2] == '\\' || v[2] == '/') &&
		(v[0]|0x20 >= 'a' && v[0]|0x20 <= 'z')
}

// Default 当前进程环境。
func Default() *Redactor { return FromEnv(os.Environ()) }

// String 脱敏一段文本，返回结果与替换次数。
func (r *Redactor) String(s string) (string, int) {
	n := 0
	for _, v := range r.values {
		if c := strings.Count(s, v); c > 0 {
			n += c
			s = strings.ReplaceAll(s, v, Placeholder)
		}
	}
	count := func(re *regexp.Regexp, repl string) {
		s = re.ReplaceAllStringFunc(s, func(m string) string {
			if strings.Contains(m, Placeholder) {
				return m
			}
			n++
			return re.ReplaceAllString(m, repl)
		})
	}
	count(rePEM, Placeholder)
	count(reBearer, "Bearer "+Placeholder)
	count(rePrefix, Placeholder)
	count(reURL, "${1}"+Placeholder+"@")
	s = reAssign.ReplaceAllStringFunc(s, func(m string) string {
		g := reAssign.FindStringSubmatch(m)
		if strings.Contains(m, Placeholder) {
			return m
		}
		var v string
		switch {
		case g[3] != "":
			v = `"` + Placeholder + `"`
		case g[4] != "":
			v = `'` + Placeholder + `'`
		default:
			if looksLikeCode(g[5]) {
				return m
			}
			v = Placeholder
		}
		n++
		return g[1] + g[2] + v
	})
	return s, n
}

// Content 脱敏内容里的文本片段（媒体与结构化数据不动）。
func (r *Redactor) Content(c session.Content) (session.Content, int) {
	total := 0
	var out session.Content
	for i, p := range c {
		if p.Kind != "text" {
			continue
		}
		t, n := r.String(p.Text)
		if n == 0 {
			continue
		}
		if out == nil {
			out = append(session.Content(nil), c...)
		}
		out[i].Text = t
		total += n
	}
	if out == nil {
		return c, 0
	}
	return out, total
}

// AttrRedacted 结果属性：本次替换次数（可观测）。
const AttrRedacted = "redacted"

// Layer 工具 Transform：结果在落库、溢出产物、回模型之前脱敏。
// 作为最外层 Transform（thin-kernel 决定 2）：内层 Guard 返回的 Confirm 原样透传；确认后重跑也会再经过它。
// 结果在 chain 返回后才写幂等记录、溢出产物与日志，所以落库的都是脱敏后的文本。脱敏失败（超时 / panic）按 FailOpen=false 处理：
// 宁可拒绝这次结果，也不把未脱敏的文本写进日志。
func Layer(r *Redactor) toolhub.Layer {
	return toolhub.Layer{
		Name:     "redact",
		Timeout:  2 * time.Second,
		FailOpen: false,
		Call: func(ctx context.Context, cx *toolhub.Cx, args json.RawMessage, next toolhub.Next) (toolhub.Output, error) {
			out, err := next(ctx, cx, args)
			if err != nil || out.Kind != toolhub.Done {
				return out, err
			}
			if c, n := r.Content(out.Content); n > 0 {
				out.Content = c
			}
			return out, nil
		},
	}
}

// looksLikeCode 不带引号的赋值右侧是不是代码：函数调用、表达式、纯标识符 / 成员访问。
// 真实密钥通常是带引号的字面量，或含数字的长串；代码误伤会让模型读到的源码和磁盘不一致。
func looksLikeCode(v string) bool {
	if strings.ContainsAny(v, "()[]{};,<>!&|+*") {
		return true
	}
	return reIdent.MatchString(v)
}

// HasPlaceholder 参数里是否带占位符（写回文件会把占位符写进源码）。
func HasPlaceholder(args json.RawMessage) bool {
	return strings.Contains(string(args), Placeholder)
}

// WriteGuard 工具 Guard：写文件类工具（带 toolhub.AttrWorkspace 的 edit / write_file）的参数里带占位符时拒绝。
// 模型读到的是脱敏后的文本，原样写回会把占位符写进文件、edit 的 old_string 也对不上磁盘内容。
func WriteGuard() toolhub.Layer {
	return toolhub.Layer{
		Name:    "redact-guard",
		Guard:   true,
		Timeout: 2 * time.Second,
		Call: func(ctx context.Context, cx *toolhub.Cx, args json.RawMessage, next toolhub.Next) (toolhub.Output, error) {
			if t := cx.Tool; t != nil && t.Attrs[toolhub.AttrWorkspace] == true && HasPlaceholder(args) {
				return toolhub.Errorf("参数里含脱敏占位符 %s：它不是文件的真实内容，不能写回。请改写不含密钥的部分，或让用户手动处理含密钥的行", Placeholder), nil
			}
			return next(ctx, cx, args)
		},
	}
}
