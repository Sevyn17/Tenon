//go:build !windows

package proc

import (
	"errors"
	"os/exec"
	"syscall"
)

// group 新进程组：killTree 向整个组发信号。
func group(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killTree 向整个进程组发 SIGKILL。
//
// 「进程已经不在了」算成功，不算失败：Kill 的语义是「确保它结束」，它已经结束就达成了目的。
// 之前直接返回 ESRCH（`no such process`），后果有两个：
//   - Supervisor.Stop() 对已经自行退出的进程返回错误，调用方以为停止失败
//     （2026-11-14 CI：TestSupervisorStopNoRestart 偶发，16 篇 D8）；
//   - 与 Windows 实现语义不一致（那边一直吞掉「已退出」）。
//
// ESRCH 之外的真实错误（比如权限不足 EPERM）照常返回。
func killTree(cmd *exec.Cmd) error {
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
