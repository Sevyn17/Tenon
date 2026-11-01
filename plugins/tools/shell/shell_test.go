package shell

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
	"time"

	"ybase-kernel/core/session"
	"ybase-kernel/core/toolhub"
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
