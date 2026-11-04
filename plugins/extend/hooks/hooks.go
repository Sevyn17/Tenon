// Package hooks 用户脚本钩子插件（session-host D5）：把工具前 / 工具后 / 回合结束
// 三个点位暴露给用户脚本。协议与 Claude Code / Codex hooks 兼容：
// stdin 收一个 JSON；退出码 0 时 stdout 可选 JSON（decision/reason/additionalContext），
// 退出码 2 = 阻止（理由取 stderr），其他非零 = 钩子自身失败，只告警不阻止。
package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"ybase-kernel/core/session"
	"ybase-kernel/core/toolhub"
)

// 事件名。
const (
	PreToolUse  = "pre_tool_use"
	PostToolUse = "post_tool_use"
	Stop        = "stop"
)

// DefaultTimeout 单个钩子默认超时（Codex 600s 偏长；钩子挡在工具前，取 60s）。
const DefaultTimeout = 60 * time.Second

// MaxOutput 钩子 stdout / stderr 各自保留的上限。
const MaxOutput = 64 << 10

// Hook 一条配置。
type Hook struct {
	Event   string `toml:"event"`
	Matcher string `toml:"matcher"` // 工具名正则（完整匹配）；空 = 全部；stop 忽略
	Command string `toml:"command"`
	Timeout int    `toml:"timeout"` // 秒
	// Sandbox 钩子命令的沙箱：read-only / workspace-write（缺省）/ danger-full-access。
	Sandbox string `toml:"sandbox"`

	re *regexp.Regexp
}

// Config 钩子配置文件。
type Config struct {
	Hook []Hook `toml:"hook"`
}

// LoadConfig 读配置；文件不存在返回空配置。
func LoadConfig(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := toml.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("hooks: %s: %w", path, err)
	}
	for i := range c.Hook {
		h := &c.Hook[i]
		switch h.Event {
		case PreToolUse, PostToolUse, Stop:
		default:
			return c, fmt.Errorf("hooks: %s: 第 %d 条 event %q 不支持（pre_tool_use / post_tool_use / stop）", path, i+1, h.Event)
		}
		if strings.TrimSpace(h.Command) == "" {
			return c, fmt.Errorf("hooks: %s: 第 %d 条缺 command", path, i+1)
		}
		if h.Sandbox != "" {
			if _, err := toolhub.ParseSandboxMode(h.Sandbox); err != nil {
				return c, fmt.Errorf("hooks: %s: 第 %d 条 sandbox：%w", path, i+1, err)
			}
		}
		if h.Matcher != "" {
			re, err := regexp.Compile("^(?:" + h.Matcher + ")$")
			if err != nil {
				return c, fmt.Errorf("hooks: %s: 第 %d 条 matcher：%w", path, i+1, err)
			}
			h.re = re
		}
	}
	return c, nil
}

// Merge 合并多份配置（按顺序执行）。
func Merge(cs ...Config) Config {
	var out Config
	for _, c := range cs {
		out.Hook = append(out.Hook, c.Hook...)
	}
	return out
}

func (h *Hook) matches(tool string) bool { return h.re == nil || h.re.MatchString(tool) }

func (h *Hook) timeout() time.Duration {
	if h.Timeout > 0 {
		return time.Duration(h.Timeout) * time.Second
	}
	return DefaultTimeout
}

// Input 发给钩子 stdin 的 JSON。
type Input struct {
	SessionID    string          `json:"session_id"`
	Event        string          `json:"hook_event_name"`
	Cwd          string          `json:"cwd"`
	ToolName     string          `json:"tool_name,omitempty"`
	CallID       string          `json:"tool_use_id,omitempty"`
	ToolInput    json.RawMessage `json:"tool_input,omitempty"`
	ToolResponse string          `json:"tool_response,omitempty"`
	ToolStatus   string          `json:"tool_status,omitempty"`
	EndReason    string          `json:"end_reason,omitempty"`
}

// output 钩子 stdout JSON（取兼容子集）。
type output struct {
	Decision          string `json:"decision"`
	Reason            string `json:"reason"`
	AdditionalContext string `json:"additionalContext"`
	SystemMessage     string `json:"systemMessage"`
	Specific          *struct {
		PermissionDecision       string `json:"permissionDecision"`
		PermissionDecisionReason string `json:"permissionDecisionReason"`
		AdditionalContext        string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// Result 一个钩子的结果。
type Result struct {
	Block   bool
	Reason  string
	Context string // 追加给模型的说明
	Warning string // 给用户看的（钩子失败 / systemMessage）
}

// Runner 执行钩子。
type Runner struct {
	Cfg  Config
	Cwd  string
	Warn func(string) // 告警输出（可空）
	// Confine 把钩子命令放进沙箱（可空 = 不加沙箱）；返回的 cleanup 在 Start 之后调用。
	Confine func(cmd *exec.Cmd, mode toolhub.SandboxMode) (func(), error)
	argv    func(string) []string
}

// New 构造。
func New(cfg Config, cwd string, warn func(string)) *Runner {
	return &Runner{Cfg: cfg, Cwd: cwd, Warn: warn, argv: defaultArgv()}
}

// psExitPre / psExitPost 让 PowerShell 的退出码等于最后一条命令的结果（与 shell 插件同一份写法）：
// 不加时原生程序的任何非零退出码都被报成 1，`python guard.py` 退出 2 也不会阻止工具——
// 守卫钩子在 Windows 上静默失效（2026-11-04 审查 P0-15）。
const (
	psExitPre  = "$global:LASTEXITCODE = 0\n"
	psExitPost = "\nif (-not $?) { if ($LASTEXITCODE) { exit $LASTEXITCODE }; exit 1 }"
)

func defaultArgv() func(string) []string {
	if runtime.GOOS == "windows" {
		bin := "powershell"
		// 商店版 pwsh（WindowsApps 下）跑在包容器里，看不到调用方的目录，见 shell.packagedApp。
		if p, err := exec.LookPath("pwsh"); err == nil && !strings.Contains(strings.ToLower(filepath.ToSlash(p)), "/windowsapps/") {
			bin = p
		}
		return func(c string) []string {
			return []string{bin, "-NoProfile", "-NonInteractive", "-Command", psExitPre + c + psExitPost}
		}
	}
	bin := "sh"
	if p, err := exec.LookPath("bash"); err == nil {
		bin = p
	}
	return func(c string) []string { return []string{bin, "-c", c} }
}

// Empty 没有任何钩子。
func (r *Runner) Empty() bool { return r == nil || len(r.Cfg.Hook) == 0 }

func (r *Runner) has(event string) bool {
	if r == nil {
		return false
	}
	for _, h := range r.Cfg.Hook {
		if h.Event == event {
			return true
		}
	}
	return false
}

// Fire 依次执行匹配 event / tool 的钩子；第一个阻止的即停。
func (r *Runner) Fire(ctx context.Context, in Input) []Result {
	in.Event = strings.TrimSpace(in.Event)
	in.Cwd = r.Cwd
	var out []Result
	for i := range r.Cfg.Hook {
		h := &r.Cfg.Hook[i]
		if h.Event != in.Event || (in.Event != Stop && !h.matches(in.ToolName)) {
			continue
		}
		res := r.run(ctx, h, in)
		if res.Warning != "" && r.Warn != nil {
			r.Warn(res.Warning)
		}
		out = append(out, res)
		if res.Block {
			break
		}
	}
	return out
}

func (r *Runner) run(ctx context.Context, h *Hook, in Input) Result {
	body, _ := json.Marshal(in)
	ctx, cancel := context.WithTimeout(ctx, h.timeout())
	defer cancel()
	a := r.argv(h.Command)
	cmd := exec.CommandContext(ctx, a[0], a[1:]...)
	cmd.Dir = r.Cwd
	cmd.Stdin = bytes.NewReader(body)
	cmd.Env = append(os.Environ(), "YBASE_HOOK_EVENT="+in.Event, "YBASE_SESSION_ID="+in.SessionID)
	cmd.WaitDelay = 2 * time.Second
	var so, se capBuf
	cmd.Stdout, cmd.Stderr = &so, &se
	name := fmt.Sprintf("钩子 %s（%s）", in.Event, clip(h.Command, 60))
	cleanup := func() {}
	if r.Confine != nil {
		mode := toolhub.SandboxWorkspaceWrite
		if h.Sandbox != "" {
			mode = toolhub.SandboxMode(h.Sandbox)
		}
		c, err := r.Confine(cmd, mode)
		if err != nil {
			return Result{Warning: fmt.Sprintf("%s 无法放进沙箱，未执行：%v", name, err)}
		}
		cleanup = c
	}
	err := cmd.Start()
	cleanup()
	if err == nil {
		err = cmd.Wait()
	}
	if ctx.Err() == context.DeadlineExceeded {
		return Result{Warning: name + " 超时，已忽略"}
	}
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			return Result{Warning: fmt.Sprintf("%s 无法启动：%v", name, err)}
		}
		code = ee.ExitCode()
	}
	switch code {
	case 0:
		s := strings.TrimSpace(so.String())
		if s == "" || !strings.HasPrefix(s, "{") {
			// 纯文本 stdout：工具后 / stop 当作补充说明，工具前忽略。
			if in.Event == PostToolUse && s != "" {
				return Result{Context: s}
			}
			return Result{}
		}
		var o output
		if err := json.Unmarshal([]byte(s), &o); err != nil {
			return Result{Warning: name + " 输出的 JSON 无效，已忽略"}
		}
		res := Result{Context: o.AdditionalContext, Warning: o.SystemMessage}
		if o.Specific != nil {
			if o.Specific.AdditionalContext != "" {
				res.Context = o.Specific.AdditionalContext
			}
			if o.Specific.PermissionDecision == "deny" {
				o.Decision, o.Reason = "block", o.Specific.PermissionDecisionReason
			}
		}
		if o.Decision == "block" {
			if strings.TrimSpace(o.Reason) == "" {
				res.Warning = name + " 返回 decision:block 但没有 reason，已忽略"
				return res
			}
			res.Block, res.Reason = true, o.Reason
		}
		return res
	case 2:
		reason := strings.TrimSpace(se.String())
		if reason == "" {
			return Result{Warning: name + " 以退出码 2 结束但 stderr 没有理由，已忽略"}
		}
		return Result{Block: true, Reason: reason}
	default:
		return Result{Warning: fmt.Sprintf("%s 失败（退出码 %d）：%s", name, code, clip(strings.TrimSpace(se.String()), 300))}
	}
}

// Layers 返回工具前（Guard）与工具后（Transform）两层；没有对应钩子的不返回。
func (r *Runner) Layers() []toolhub.Layer {
	var ls []toolhub.Layer
	if r.has(PreToolUse) {
		ls = append(ls, toolhub.Layer{
			Name:    "hooks-pre",
			Guard:   true,
			Timeout: r.total(PreToolUse),
			Call: func(ctx context.Context, cx *toolhub.Cx, args json.RawMessage, next toolhub.Next) (toolhub.Output, error) {
				for _, res := range r.Fire(ctx, Input{SessionID: cx.SessionID, Event: PreToolUse, ToolName: cx.Tool.Spec.Name, CallID: cx.CallID, ToolInput: args}) {
					if res.Block {
						return toolhub.Errorf("被用户钩子阻止：%s", res.Reason), nil
					}
				}
				return next(ctx, cx, args)
			},
		})
	}
	if r.has(PostToolUse) {
		ls = append(ls, toolhub.Layer{
			Name:     "hooks-post",
			Timeout:  r.total(PostToolUse),
			FailOpen: true,
			Call: func(ctx context.Context, cx *toolhub.Cx, args json.RawMessage, next toolhub.Next) (toolhub.Output, error) {
				out, err := next(ctx, cx, args)
				// 只对真正执行成功的结果触发：本层在所有 Guard 之外，被拒绝 / 出错的结果不送钩子（与 Claude Code PostToolUse 一致）
				if err != nil || out.Kind != toolhub.Done || out.Status != session.ToolOK {
					return out, err
				}
				in := Input{SessionID: cx.SessionID, Event: PostToolUse, ToolName: cx.Tool.Spec.Name, CallID: cx.CallID, ToolInput: args,
					ToolResponse: clip(textOf(out.Content), MaxOutput), ToolStatus: out.Status.State}
				var notes []string
				for _, res := range r.Fire(ctx, in) {
					if res.Block {
						notes = append(notes, "[用户钩子反馈] "+res.Reason)
					} else if res.Context != "" {
						notes = append(notes, "[用户钩子] "+res.Context)
					}
				}
				if len(notes) > 0 {
					out.Content = append(append(session.Content{}, out.Content...), session.NewTextPart("\n"+strings.Join(notes, "\n")))
				}
				return out, nil
			},
		})
	}
	return ls
}

// AfterTurn 回合结束钩子（只通知；P0 不支持 Codex 的 stop 续跑）。
func (r *Runner) AfterTurn(id string, end session.EndReason) {
	if !r.has(Stop) {
		return
	}
	r.Fire(context.Background(), Input{SessionID: id, Event: Stop, EndReason: end.String()})
}

// total 层超时 = 该事件全部钩子超时之和 + 余量（层超时不得早于钩子自身超时）。
func (r *Runner) total(event string) time.Duration {
	var d time.Duration
	for i := range r.Cfg.Hook {
		if r.Cfg.Hook[i].Event == event {
			d += r.Cfg.Hook[i].timeout()
		}
	}
	return d + 5*time.Second
}

func textOf(c session.Content) string {
	var b strings.Builder
	for _, p := range c {
		b.WriteString(p.Text)
	}
	return b.String()
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

type capBuf struct{ bytes.Buffer }

func (b *capBuf) Write(p []byte) (int, error) {
	if room := MaxOutput - b.Len(); room > 0 {
		if len(p) > room {
			b.Buffer.Write(p[:room])
		} else {
			b.Buffer.Write(p)
		}
	}
	return len(p), nil
}
