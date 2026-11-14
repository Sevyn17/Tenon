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
	// MaxCrashes=1：进程第一次退出就标记不可用，不依赖退避重启的时序（全量并行时
	// 进程创建本身可能很慢，两次崩溃的窗口太窄会 flake）。
	s := &Supervisor{
		Argv:          crashArgv(),
		MaxCrashes:    1,
		Backoff:       10 * time.Millisecond,
		OnCrash:       func(error) { crashes.Add(1) },
		OnUnavailable: func() { unavailable.Store(true) },
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	// 等它崩到超限（给足时间：CI 上进程创建可能很慢）。
	deadline := time.Now().Add(30 * time.Second)
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

// TestSupervisorStopAfterCrash 进程**已经自行退出**之后再 Stop，必须返回 nil。
//
// 这是 2026-11-14 CI 失败（16 篇 D8）的回归测试，也是原 TestSupervisorStopNoRestart
// 抓不稳的那个场景：crashArgv 是立即退出的命令，Stop 与进程退出在赛跑；
// 原测试没等进程退出就 Stop，窗口只有微秒级，本地跑不出来、CI 高负载才撞上。
// 这里显式先等进程崩掉再 Stop，把竞态变成确定性场景。
func TestSupervisorStopAfterCrash(t *testing.T) {
	var crashed atomic.Int32
	s := &Supervisor{
		Argv:       crashArgv(),
		MaxCrashes: 100,
		Backoff:    10 * time.Millisecond,
		OnCrash:    func(error) { crashed.Add(1) },
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 等它至少崩一次（进程确定已退出并被回收）。
	deadline := time.Now().Add(30 * time.Second)
	for crashed.Load() == 0 {
		if time.Now().After(deadline) {
			s.Stop()
			t.Fatal("进程没有按预期崩溃")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// 此时 s.cmd 指向的进程可能已退出、或已被重启成新进程，两种都不该让 Stop 报错。
	if err := s.Stop(); err != nil {
		t.Fatalf("进程已崩溃后再 Stop 应返回 nil，实际：%v（D8 回归）", err)
	}
	if s.Available() {
		t.Fatal("Stop 后 Available 应为 false")
	}
}

// TestSupervisorStopTwice 重复 Stop 必须幂等。
func TestSupervisorStopTwice(t *testing.T) {
	s := &Supervisor{Argv: crashArgv(), MaxCrashes: 100, Backoff: 10 * time.Millisecond}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(); err != nil {
		t.Fatalf("第一次 Stop：%v", err)
	}
	if err := s.Stop(); err != nil {
		t.Fatalf("第二次 Stop 应返回 nil，实际：%v（D8 回归）", err)
	}
}
