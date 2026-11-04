package hooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"ybase/runtime/session"
	"ybase/runtime/toolhub"
)

const modePrefix = "hookmode:"

// 测试用 re-exec：把本测试二进制当钩子脚本，行为由参数 hookmode:<模式> 决定。
func TestMain(m *testing.M) {
	for _, a := range os.Args[1:] {
		if strings.HasPrefix(a, modePrefix) {
			runFake(strings.TrimPrefix(a, modePrefix))
			return
		}
	}
	os.Exit(m.Run())
}

func runFake(mode string) {
	var in Input
	_ = json.NewDecoder(os.Stdin).Decode(&in)
	if p := os.Getenv("HOOK_LOG"); p != "" {
		f, _ := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		b, _ := json.Marshal(in)
		f.Write(append(b, '\n'))
		f.Close()
	}
	switch mode {
	case "exit2":
		os.Stderr.WriteString("禁止删除")
		os.Exit(2)
	case "json-block":
		os.Stdout.WriteString(`{"decision":"block","reason":"先跑测试"}`)
	case "json-deny":
		os.Stdout.WriteString(`{"hookSpecificOutput":{"permissionDecision":"deny","permissionDecisionReason":"不许"}}`)
	case "block-noreason":
		os.Stdout.WriteString(`{"decision":"block"}`)
	case "context":
		os.Stdout.WriteString(`{"additionalContext":"lint 通过"}`)
	case "slow":
		time.Sleep(2 * time.Second)
	case "fail":
		os.Stderr.WriteString("boom")
		os.Exit(3)
	case "envkey": // 能看到宿主密钥就阻止（脱敏测试）
		if os.Getenv("HOOKTEST_API_KEY") != "" {
			os.Stderr.WriteString("看到了密钥")
			os.Exit(2)
		}
	}
	os.Exit(0)
}

func runner(t *testing.T, hs ...Hook) (*Runner, *[]string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for i := range hs {
		if hs[i].Matcher != "" {
			hs[i].re = regexp.MustCompile("^(?:" + hs[i].Matcher + ")$")
		}
	}
	var warns []string
	r := New(Config{Hook: hs}, t.TempDir(), func(s string) { warns = append(warns, s) })
	// Command 字段写成模式名，argv 把它换成 re-exec 本二进制。
	r.argv = func(mode string) []string { return []string{self, modePrefix + mode} }
	return r, &warns
}

func tool(name string) *toolhub.Tool {
	return &toolhub.Tool{Spec: session.ToolSpec{Name: name}}
}

// call 用层链跑一次工具，inner 是真正的工具。
func call(t *testing.T, ls []toolhub.Layer, name string, inner toolhub.Next) (toolhub.Output, bool) {
	t.Helper()
	ran := false
	next := func(ctx context.Context, cx *toolhub.Cx, a json.RawMessage) (toolhub.Output, error) {
		ran = true
		return inner(ctx, cx, a)
	}
	for i := len(ls) - 1; i >= 0; i-- {
		l, n := ls[i], next
		next = func(ctx context.Context, cx *toolhub.Cx, a json.RawMessage) (toolhub.Output, error) {
			return l.Call(ctx, cx, a, n)
		}
	}
	out, err := next(context.Background(), &toolhub.Cx{SessionID: "s1", CallID: "c1", Tool: tool(name)}, json.RawMessage(`{"command":"rm -rf x"}`))
	if err != nil {
		t.Fatal(err)
	}
	return out, ran
}

func ok(context.Context, *toolhub.Cx, json.RawMessage) (toolhub.Output, error) {
	return toolhub.Text("done"), nil
}

func text(o toolhub.Output) string { return textOf(o.Content) }

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	c, err := LoadConfig(filepath.Join(dir, "none.toml"))
	if err != nil || len(c.Hook) != 0 {
		t.Fatalf("缺文件应为空配置: %v %v", c, err)
	}
	p := filepath.Join(dir, "hooks.toml")
	os.WriteFile(p, []byte("[[hook]]\nevent = \"pre_tool_use\"\nmatcher = \"shell|edit\"\ncommand = \"x\"\n"), 0o644)
	c, err = LoadConfig(p)
	if err != nil || len(c.Hook) != 1 || !c.Hook[0].matches("shell") || c.Hook[0].matches("shellx") {
		t.Fatalf("matcher 应完整匹配: %v %v", c, err)
	}
	for _, bad := range []string{
		"[[hook]]\nevent = \"bogus\"\ncommand = \"x\"\n",
		"[[hook]]\nevent = \"stop\"\n",
		"[[hook]]\nevent = \"pre_tool_use\"\nmatcher = \"(\"\ncommand = \"x\"\n",
	} {
		os.WriteFile(p, []byte(bad), 0o644)
		if _, err := LoadConfig(p); err == nil {
			t.Fatalf("应报错: %q", bad)
		}
	}
}

func TestPreBlocks(t *testing.T) {
	for mode, want := range map[string]string{"exit2": "禁止删除", "json-block": "先跑测试", "json-deny": "不许"} {
		r, _ := runner(t, Hook{Event: PreToolUse, Matcher: "shell", Command: mode})
		out, ran := call(t, r.Layers(), "shell", ok)
		if ran || out.Status != session.ToolError || !strings.Contains(text(out), want) {
			t.Fatalf("%s: 应阻止 ran=%v out=%q", mode, ran, text(out))
		}
	}
}

func TestPreAllowAndMatcher(t *testing.T) {
	r, warns := runner(t,
		Hook{Event: PreToolUse, Matcher: "edit", Command: "exit2"},
		Hook{Event: PreToolUse, Command: "allow"})
	out, ran := call(t, r.Layers(), "shell", ok)
	if !ran || text(out) != "done" || len(*warns) != 0 {
		t.Fatalf("不匹配的钩子不应生效: ran=%v %q %v", ran, text(out), *warns)
	}
}

func TestFailuresDoNotBlock(t *testing.T) {
	for _, mode := range []string{"fail", "block-noreason"} {
		r, warns := runner(t, Hook{Event: PreToolUse, Command: mode})
		out, ran := call(t, r.Layers(), "shell", ok)
		if !ran || text(out) != "done" || len(*warns) != 1 {
			t.Fatalf("%s: 钩子失败只告警: ran=%v %q %v", mode, ran, text(out), *warns)
		}
	}
}

func TestPostAppendsContext(t *testing.T) {
	log := filepath.Join(t.TempDir(), "log.jsonl")
	t.Setenv("HOOK_LOG", log)
	r, _ := runner(t, Hook{Event: PostToolUse, Command: "context"})
	out, ran := call(t, r.Layers(), "edit", ok)
	if !ran || !strings.Contains(text(out), "done") || !strings.Contains(text(out), "[用户钩子] lint 通过") {
		t.Fatalf("工具后钩子应追加说明: %q", text(out))
	}
	b, _ := os.ReadFile(log)
	var in Input
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(b))), &in); err != nil {
		t.Fatal(err)
	}
	if in.Event != PostToolUse || in.ToolName != "edit" || in.ToolResponse != "done" || in.ToolStatus != "ok" || in.SessionID != "s1" || in.CallID != "c1" {
		t.Fatalf("stdin 字段不对: %+v", in)
	}
}

func TestStopFires(t *testing.T) {
	log := filepath.Join(t.TempDir(), "log.jsonl")
	t.Setenv("HOOK_LOG", log)
	r, _ := runner(t, Hook{Event: Stop, Matcher: "ignored", Command: "allow"})
	if len(r.Layers()) != 0 {
		t.Fatal("只有 stop 钩子时不应加工具层")
	}
	r.AfterTurn("s9", session.DoneReason())
	r.Wait()
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), `"end_reason":"Done"`) || !strings.Contains(string(b), `"session_id":"s9"`) {
		t.Fatalf("stop 钩子输入不对: %s", b)
	}
}

func TestEmpty(t *testing.T) {
	var r *Runner
	if !r.Empty() || len(New(Config{}, ".", nil).Layers()) != 0 {
		t.Fatal("空配置不应产生层")
	}
}

// 走真实的 shell（Windows 上是 PowerShell）：钩子是原生程序、退出码 2 时要阻止
// （审查 P0-15：原先 PowerShell 把原生程序的非零退出码都报成 1，守卫钩子静默失效）。
func TestPreBlocksNativeExit2ThroughShell(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := "'" + strings.ReplaceAll(self, "'", "''") + "' " + modePrefix + "exit2"
	if runtime.GOOS == "windows" {
		cmd = "& " + cmd
	}
	r := New(Config{Hook: []Hook{{Event: PreToolUse, Matcher: "shell", Command: cmd, re: regexp.MustCompile("^(?:shell)$")}}},
		t.TempDir(), func(string) {})
	out, ran := call(t, r.Layers(), "shell", ok)
	if ran || out.Status != session.ToolError || !strings.Contains(text(out), "禁止删除") {
		t.Fatalf("原生程序退出码 2 应阻止: ran=%v out=%q", ran, text(out))
	}
}

// 第三方来源（项目目录、插件包）的钩子拿不到宿主密钥；用户级钩子照常（审查 P1-6）。
func TestThirdPartyHookEnvScrubbed(t *testing.T) {
	t.Setenv("HOOKTEST_API_KEY", "sk-host")
	r, _ := runner(t, Hook{Event: PreToolUse, Command: "envkey", scrub: true})
	if _, ran := call(t, r.Layers(), "shell", ok); !ran {
		t.Fatal("第三方钩子不应看到宿主密钥")
	}
	r, _ = runner(t, Hook{Event: PreToolUse, Command: "envkey"})
	if _, ran := call(t, r.Layers(), "shell", ok); ran {
		t.Fatal("用户级钩子应看到完整环境（测试自检）")
	}
}

// stop 钩子异步执行，不卡回合（审查 P1-30：原先同步跑在会话 actor 里，每个最长 60 s）。
func TestStopHookAsync(t *testing.T) {
	r, _ := runner(t, Hook{Event: Stop, Command: "slow"})
	start := time.Now()
	r.AfterTurn("s1", session.DoneReason())
	if d := time.Since(start); d > time.Second {
		t.Fatalf("AfterTurn 应立即返回，用了 %v", d)
	}
	r.Wait()
	if d := time.Since(start); d < 2*time.Second {
		t.Fatalf("Wait 应等到钩子结束，只等了 %v", d)
	}
}
