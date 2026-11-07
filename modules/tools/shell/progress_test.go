package shell

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"ybase/runtime/toolhub"
)

// 前台命令运行期间，输出尾部经 cx.Progress 报出来（界面实时显示）；命令结束后不再报。
func TestShellProgress(t *testing.T) {
	old := ProgressEvery
	ProgressEvery = 50 * time.Millisecond
	t.Cleanup(func() { ProgressEvery = old })
	s := NewShell(t.TempDir())
	cmd := "echo first; sleep 1; echo second"
	if runtime.GOOS == "windows" {
		cmd = "Write-Output first; Start-Sleep -Milliseconds 1000; Write-Output second"
	}
	var mu sync.Mutex
	var got []string
	cx := &toolhub.Cx{Progress: func(text string) {
		mu.Lock()
		got = append(got, text)
		mu.Unlock()
	}}
	out, err := shellTool(s, "shell").Call(context.Background(), cx, json.RawMessage(`{"command":`+q(cmd)+`}`))
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	n := len(got)
	seen := strings.Join(got, "|")
	mu.Unlock()
	if n == 0 || !strings.Contains(seen, "first") {
		t.Fatalf("运行中应报出 first：%q（结果 %v）", got, out.Content)
	}
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != n {
		t.Fatalf("结束后不应再报：%d → %d", n, len(got))
	}
}

func TestTailLines(t *testing.T) {
	if got := tailLines("a\r\nb\nc\n\n", 2); got != "b\nc" {
		t.Fatalf("%q", got)
	}
	if got := tailLines("x", 5); got != "x" {
		t.Fatalf("%q", got)
	}
}
