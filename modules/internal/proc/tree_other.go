//go:build !windows

package proc

import (
	"os/exec"
	"syscall"
)

// group 新进程组：killTree 向整个组发信号。
func group(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killTree 向整个进程组发 SIGKILL。
func killTree(cmd *exec.Cmd) error {
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
