//go:build windows

package proc

import (
	"os/exec"
	"strconv"
	"syscall"
)

// group 新进程组（taskkill /T 按进程树杀，不依赖进程组，但新组让 Ctrl-C 不波及宿主）。
func group(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// killTree 用 taskkill /T 杀整个进程树；taskkill 失败时退回只杀主进程。
func killTree(cmd *exec.Cmd) error {
	k := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	if err := k.Run(); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}
