//go:build !windows

package proc

import "os/exec"

// NoWindow 让 cmd 启动时不弹控制台窗口（只在 Windows 上有作用，见 nowin_windows.go）；本平台为空操作。
func NoWindow(cmd *exec.Cmd) {}
