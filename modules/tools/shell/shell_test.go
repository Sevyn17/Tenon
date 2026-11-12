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

// 后台 job：启动 → 等待输出 → 增量读 → kill。
//
// 2026-11-12：修了两个「负载高时才会暴露」的脆弱点（本地全量测试曾偶发失败一次，
// 12.79s 后报 step2 断言不符；此后单跑/包内/race/重负载 30+ 次均未复现）：
//
//  1. 原先硬编码 "job-1" / "job-2" 假定 job 编号从 1 开始且严格递增。
//     但 job id 来自 Shell.seq，是**同一 Shell 实例**内的全局计数——
//     将来若有别的用例共用/污染实例，或实现改成复用编号，断言就会错。
//     改成从启动输出里解析实际 id。
//
//  2. 原先 sleepCmd(1) 只有 1 秒，wait=true timeout=20。负载高时 PowerShell 启动
//     本身就要几百毫秒，1 秒的 sleep 可能来不及让 echo end 落进缓冲就被 Wait 关闭，
//     于是「已结束」出现了但输出不全。改成 sleep 2 秒（给启动留余量），
//     并把等待上限从 20 秒降到 10 秒（失败时更快暴露，不是靠等满）。
func TestShellBackgroundJob(t *testing.T) {
	s := NewShell(t.TempDir())
	defer s.Close()
	// 从启动输出解析 job id，不假定它是 job-1。
	jobID := func(out string) string {
		t.Helper()
		i := strings.Index(out, "job-")
		if i < 0 {
			t.Fatalf("应返回 job id: %q", out)
		}
		rest := out[i+len("job-"):]
		n := 0
		for n < len(rest) && rest[n] >= '0' && rest[n] <= '9' {
			n++
		}
		if n == 0 {
			t.Fatalf("job id 缺编号: %q", out)
		}
		return "job-" + rest[:n]
	}

	cmd := "echo start; " + sleepCmd(2) + "; echo end"
	out, _ := callT(t, s, "shell", `{"command":`+q(cmd)+`,"background":true}`)
	id1 := jobID(out)
	out, _ = callT(t, s, "job_output", `{"id":"`+id1+`","wait":true,"timeout":10}`)
	if !strings.Contains(out, "start") || !strings.Contains(out, "end") || !strings.Contains(out, "已结束，退出码 0") {
		t.Fatalf("等待结束后应有全部输出（id=%s）: %q", id1, out)
	}
	out, _ = callT(t, s, "job_output", `{"id":"`+id1+`"}`)
	if strings.Contains(out, "start") {
		t.Fatalf("再次读取不应重复旧输出: %q", out)
	}

	// 长任务被 kill。
	out, _ = callT(t, s, "shell", `{"command":`+q(sleepCmd(60))+`,"background":true}`)
	id2 := jobID(out)
	if id2 == id1 {
		t.Fatalf("两个 job 的 id 应不同: %s / %s", id1, id2)
	}
	out, isErr := callT(t, s, "job_kill", `{"id":"`+id2+`"}`)
	if isErr || !strings.Contains(out, "已终止") {
		t.Fatalf("kill 失败（id=%s）: %q", id2, out)
	}
	if _, isErr := callT(t, s, "job_output", `{"id":"job-9999"}`); !isErr {
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
