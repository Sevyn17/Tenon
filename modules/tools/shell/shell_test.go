package shell

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

func shellTool(s *Shell, name string) *toolhub.Tool {
	for _, t := range s.Tools() {
		if t.Spec.Name == name {
			return t
		}
	}
	return nil
}

func callT(t *testing.T, s *Shell, name, args string) (string, bool) {
	t.Helper()
	out, err := shellTool(s, name).Call(context.Background(), &toolhub.Cx{}, json.RawMessage(args))
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	for _, p := range out.Content {
		sb.WriteString(p.Text)
	}
	return sb.String(), out.Status == session.ToolError
}

func q(s string) string { b, _ := json.Marshal(s); return string(b) }

// 平台无关的命令片段。
func sleepCmd(sec int) string {
	if runtime.GOOS == "windows" {
		return "Start-Sleep -Seconds " + itoa(sec)
	}
	return "sleep " + itoa(sec)
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

// shell 命令在工作目录里执行，不论用哪个 shell（2026-11-03 演示：商店版 pwsh 不继承进程当前目录，
// 相对路径全部落到它的安装目录，模型误判 write_file「没写进去」）。工作目录名带空格与引号。
func TestShellRunsInWorkdir(t *testing.T) {
	// 放在 %LOCALAPPDATA% 下：商店版 pwsh 对这里做了包内虚拟化，复现条件与演示时一致
	// （%TEMP% 下的 t.TempDir() 复现不出来）。
	base := t.TempDir()
	if la := os.Getenv("LOCALAPPDATA"); runtime.GOOS == "windows" && la != "" {
		var err error
		if base, err = os.MkdirTemp(la, "tenon-shelltest-"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(base) })
	}
	root := filepath.Join(base, `my 'proj'`)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "marker.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewShell(root)
	cmd := "ls marker.md"
	if runtime.GOOS == "windows" {
		cmd = "(Get-Location).Path; Test-Path marker.md"
	}
	out, isErr := callT(t, s, "shell", `{"command":`+q(cmd)+`}`)
	if isErr {
		t.Fatalf("命令失败：%q", out)
	}
	if runtime.GOOS == "windows" && (!strings.Contains(out, root) || !strings.Contains(out, "True")) {
		t.Fatalf("应在工作目录 %s 里执行：%q", root, out)
	}
}

func TestShellForegroundUTF8AndExitCode(t *testing.T) {
	s := NewShell(t.TempDir())
	out, isErr := callT(t, s, "shell", `{"command":`+q("echo 你好")+`}`)
	if isErr || !strings.Contains(out, "你好") || !strings.Contains(out, "[退出码 0]") {
		t.Fatalf("中文输出或退出码错: %q", out)
	}
	out, isErr = callT(t, s, "shell", `{"command":"exit 3"}`)
	if !isErr || !strings.Contains(out, "[退出码 3]") {
		t.Fatalf("非零退出应为错误并带退出码: %q", out)
	}
}

func TestShellTimeoutKillsTree(t *testing.T) {
	s := NewShell(t.TempDir())
	start := time.Now()
	out, isErr := callT(t, s, "shell", `{"command":`+q(sleepCmd(30))+`,"timeout":1}`)
	if !isErr || !strings.Contains(out, "超时") {
		t.Fatalf("应超时: %q", out)
	}
	if d := time.Since(start); d > 15*time.Second {
		t.Fatalf("超时后未及时返回: %v", d)
	}
}

func TestShellBackgroundJob(t *testing.T) {
	s := NewShell(t.TempDir())
	defer s.Close()
	cmd := "echo start; " + sleepCmd(1) + "; echo end"
	out, _ := callT(t, s, "shell", `{"command":`+q(cmd)+`,"background":true}`)
	if !strings.Contains(out, "job-1") {
		t.Fatalf("应返回 job id: %q", out)
	}
	out, _ = callT(t, s, "job_output", `{"id":"job-1","wait":true,"timeout":20}`)
	if !strings.Contains(out, "start") || !strings.Contains(out, "end") || !strings.Contains(out, "已结束，退出码 0") {
		t.Fatalf("等待结束后应有全部输出: %q", out)
	}
	out, _ = callT(t, s, "job_output", `{"id":"job-1"}`)
	if strings.Contains(out, "start") {
		t.Fatalf("再次读取不应重复旧输出: %q", out)
	}
	// 长任务被 kill。
	callT(t, s, "shell", `{"command":`+q(sleepCmd(60))+`,"background":true}`)
	out, isErr := callT(t, s, "job_kill", `{"id":"job-2"}`)
	if isErr || !strings.Contains(out, "已终止") {
		t.Fatalf("kill 失败: %q", out)
	}
	if _, isErr := callT(t, s, "job_output", `{"id":"job-9"}`); !isErr {
		t.Fatal("不存在的 job 应报错")
	}
}

func TestTailBuf(t *testing.T) {
	b := &tailBuf{cap: 4}
	b.Write([]byte("abcdef"))
	s, off, dropped := b.since(0)
	if s != "cdef" || off != 6 || dropped != 2 {
		t.Fatalf("得 %q %d %d", s, off, dropped)
	}
	b.Write([]byte("g"))
	if s, _, _ := b.since(off); s != "g" {
		t.Fatalf("增量读错: %q", s)
	}
}

// 原生程序的退出码原样报给模型（审查 P0-15：Windows PowerShell 原先一律报 1）。
func TestShellNativeExitCode(t *testing.T) {
	s := NewShell(t.TempDir())
	cmd := "cmd /c exit 7"
	if runtime.GOOS != "windows" {
		cmd = "sh -c 'exit 7'"
	}
	out, isErr := callT(t, s, "shell", `{"command":`+q(cmd)+`}`)
	if !isErr || !strings.Contains(out, "[退出码 7]") {
		t.Fatalf("原生程序退出码应为 7: %q", out)
	}
	if runtime.GOOS == "windows" {
		out, isErr = callT(t, s, "shell", `{"command":`+q("cmd /c exit 7; echo after")+`}`)
		if isErr || !strings.Contains(out, "[退出码 0]") {
			t.Fatalf("最后一条成功时退出码应为 0: %q", out)
		}
	}
}

// 后台 job 属于启动它的会话；结束且输出读完后清理（2026-11-04 审查 P1-9）。
func TestJobScopedToSession(t *testing.T) {
	s := NewShell(t.TempDir())
	defer s.Close()
	call := func(sid, name, args string) string {
		out, err := shellTool(s, name).Call(context.Background(), &toolhub.Cx{SessionID: sid}, json.RawMessage(args))
		if err != nil {
			t.Fatal(err)
		}
		var sb strings.Builder
		for _, p := range out.Content {
			sb.WriteString(p.Text)
		}
		return sb.String()
	}
	if out := call("A", "shell", `{"command":`+q("echo hi")+`,"background":true}`); !strings.Contains(out, "job-1") {
		t.Fatalf("应启动 job-1: %q", out)
	}
	if out := call("B", "job_output", `{"id":"job-1","wait":true,"timeout":20}`); !strings.Contains(out, "不存在") {
		t.Fatalf("别的会话不应读到: %q", out)
	}
	if out := call("B", "job_kill", `{"id":"job-1"}`); !strings.Contains(out, "不存在") {
		t.Fatalf("别的会话不应杀掉: %q", out)
	}
	if out := call("A", "job_output", `{"id":"job-1","wait":true,"timeout":20}`); !strings.Contains(out, "hi") || !strings.Contains(out, "已结束") {
		t.Fatalf("本会话应读到输出: %q", out)
	}
	s.mu.Lock()
	n := len(s.jobs)
	s.mu.Unlock()
	if n != 0 {
		t.Fatalf("结束且读完的 job 应清理，还剩 %d 个", n)
	}
}
