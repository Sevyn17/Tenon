package extproc

// 进程监管测试：崩溃退避重启、超限不可用、主动停止不重启。
// 用一个立即退出的命令（go run 太重，用系统命令）。Windows / Unix 分别给。

import (
	"context"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// crashArgv 返回一个「立即成功退出」的命令（模拟进程退出）。
func crashArgv() []string {
	if runtime.GOOS == "windows" {
		return []string{"cmd", "/c", "exit", "1"}
	}
	return []string{"sh", "-c", "exit 1"}
}

func TestSupervisorUnavailableAfterCrashes(t *testing.T) {
	var crashes atomic.Int32
	var unavailable atomic.Bool
	s := &Supervisor{
		Argv:          crashArgv(),
		MaxCrashes:    2,
		Backoff:       10 * time.Millisecond,
		OnCrash:       func(error) { crashes.Add(1) },
		OnUnavailable: func() { unavailable.Store(true) },
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	// 等它崩到超限。
	deadline := time.Now().Add(10 * time.Second)
	for !unavailable.Load() {
		if time.Now().After(deadline) {
			t.Fatalf("超限后应标记不可用（crashes=%d）", crashes.Load())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if s.Available() {
		t.Fatal("标记不可用后 Available 应为 false")
	}
}

func TestSupervisorStopNoRestart(t *testing.T) {
	s := &Supervisor{Argv: crashArgv(), MaxCrashes: 100, Backoff: 10 * time.Millisecond}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
	if s.Available() {
		t.Fatal("Stop 后 Available 应为 false")
	}
}
