package proc

import (
	"os/exec"
	"runtime"
	"testing"
	"time"
)

// 立即退出的命令（跨平台）。Kill 一个已经自己退出的进程是合法场景：
// Supervisor.Stop() 会在插件进程崩了之后被调用，exec.CommandContext 的 Cancel
// 也可能在进程刚退出时触发。
func quickExitArgv() []string {
	if runtime.GOOS == "windows" {
		return []string{"cmd", "/c", "exit", "1"}
	}
	return []string{"sh", "-c", "exit 1"}
}

// TestKillAlreadyExited 「进程已经退出」时 Kill 必须返回 nil。
//
// 这是 2026-11-14 CI 失败（16 篇 D8）的回归测试：Unix 侧 killTree 原先直接返回
// syscall.Kill 的错误，进程已退出并回收时是 ESRCH（`no such process`），
// 于是 Supervisor.Stop() 报错；Windows 侧一直吞掉这个错误，两边语义不一致。
//
// 要稳定复现「已退出」而不是靠竞态：先 Start 再 Wait 到进程结束，然后才 Kill。
// 注意 Group 之后进程在独立进程组里，主进程退出后组可能已空，正是要测的情形。
//
// 平台说明：Windows 的 killTree 本来就不把「已退出」当错误，所以这条在 Windows 上
// 修复前后都过——它守的是 Unix 分支。Unix 分支的行为由 CI 的 Linux runner 验证
// （本地没有 Linux 环境，2026-11-14 记录）。
func TestKillAlreadyExited(t *testing.T) {
	cmd := exec.Command(quickExitArgv()[0], quickExitArgv()[1:]...)
	Group(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// 等进程真正退出并被回收：Wait 返回后进程已不在，但 cmd.Process 仍非 nil
	// （这正是 Kill 会去发信号的状态）。
	if err := cmd.Wait(); err != nil {
		// 退出码 1 是预期的；只要 Wait 返回就说明进程已结束。
		t.Logf("进程按预期非零退出：%v", err)
	}
	// 给内核一点时间回收（Unix 上僵尸被收割后 pid 才真正消失）。
	time.Sleep(50 * time.Millisecond)
	if err := Kill(cmd); err != nil {
		t.Fatalf("对已退出的进程 Kill 应返回 nil，实际：%v（D8 回归）", err)
	}
}

// TestKillTwice 连续 Kill 同一进程两次：第二次进程早已不在，仍须返回 nil。
// 这条在**两个平台**都有效（不依赖 ESRCH 的具体错误值）：
// Unix 上第二次会命中 ESRCH，Windows 上会命中「主进程已退出」，
// 两者都必须被当成成功——这正是 D8 要求的统一语义。
func TestKillTwice(t *testing.T) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd.exe", "/c", "ping -n 60 127.0.0.1")
	} else {
		cmd = exec.Command("sh", "-c", "sleep 30; true")
	}
	Group(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if err := Kill(cmd); err != nil {
		t.Fatalf("第一次 Kill 应成功，实际：%v", err)
	}
	// 等进程真正结束（Wait 之后 pid 才会被回收）。
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("第一次 Kill 后进程仍活着")
	}
	time.Sleep(50 * time.Millisecond)
	// 第二次：进程已不在，必须仍返回 nil（D8 的核心语义）。
	if err := Kill(cmd); err != nil {
		t.Fatalf("对已结束的进程再次 Kill 应返回 nil，实际：%v（D8 回归）", err)
	}
}

// TestKillRunningStillWorks 对**还活着**的进程 Kill 必须成功，并且真的杀掉。
// 防止上面那条修复把「已退出不算错」写成「永远不算错」。
func TestKillRunningStillWorks(t *testing.T) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd.exe", "/c", "ping -n 60 127.0.0.1")
	} else {
		cmd = exec.Command("sh", "-c", "sleep 30; true")
	}
	Group(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond) // 让进程真正跑起来
	if err := Kill(cmd); err != nil {
		t.Fatalf("对运行中的进程 Kill 应成功，实际：%v", err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("Kill 返回成功但进程仍活着")
	}
}

// TestKillNotStarted Start 之前 Kill 什么都不做，也不报错。
func TestKillNotStarted(t *testing.T) {
	cmd := exec.Command(quickExitArgv()[0], quickExitArgv()[1:]...)
	Group(cmd)
	if err := Kill(cmd); err != nil {
		t.Fatalf("未启动的命令 Kill 应返回 nil，实际：%v", err)
	}
	if err := Kill(nil); err != nil {
		t.Fatalf("nil 应返回 nil，实际：%v", err)
	}
}
