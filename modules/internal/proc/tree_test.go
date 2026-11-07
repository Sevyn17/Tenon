package proc

import (
	"bytes"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

// 杀进程树：shell 起的孙进程也要一起结束。只杀主进程时孙进程还占着 stdout 管道，
// Wait 要等它自己退出（2026-11-04 审查 P1-29：hooks 超时留下孙进程、mcp 关闭卡在读协程）。
func TestKillTree(t *testing.T) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd.exe", "/c", "ping -n 60 127.0.0.1") // 比下面的等待上限长：没杀掉时一定超时
	} else {
		cmd = exec.Command("sh", "-c", "sleep 30; true") // ; true：不让 sh 直接 exec 成 sleep
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out // 非 *os.File：Wait 要等管道读完
	Group(cmd)                          // exec.Command 构造（同 mcp）：只设进程组，不设 Cancel
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond) // 让孙进程起来
	start := time.Now()
	if err := Kill(cmd); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	// 等待上限放宽到 20s：负载高时结束进程本身可能要几秒（Windows 的调度、扫描），但只要孙进程真的没了，
	// Wait 会立刻返回；它要是还活着，会占着管道到 ping 结束（59s），一定超过上限。
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatalf("杀进程树后 Wait 仍未返回（孙进程还占着管道），已等 %v", time.Since(start))
	}
}
