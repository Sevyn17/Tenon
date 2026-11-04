//go:build windows

package shell

import (
	"os/exec"
	"strconv"
	"syscall"
)

// setTree 取消时用 taskkill /T 杀整个进程树。
func setTree(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		k := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
		if err := k.Run(); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}
