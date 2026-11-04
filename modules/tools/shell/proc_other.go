//go:build !windows

package shell

import (
	"os/exec"
	"syscall"
)

// setTree 新进程组；取消时向整个组发 SIGKILL。
func setTree(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
