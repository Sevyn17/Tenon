//go:build windows

package proc

import (
	"os/exec"
	"syscall"
)

// createNoWindow CREATE_NO_WINDOW：子进程不分配控制台（syscall 包没有导出这个常量）。
const createNoWindow = 0x08000000

// NoWindow 让 cmd 启动时不弹控制台窗口：GUI 进程启动命令行子进程时，系统默认为它新建 conhost，
// 用户会看到黑窗一闪（2026-11-26 Mortise 桌面端启动时 checkpoint 的 git rev-parse）。
// 只用于输出走管道、不需要控制台的后台子进程；终端 / pty 不要用。
// 合并已有的 SysProcAttr（Token、CmdLine 与其他 CreationFlags 保留），与 Group / SetTree / 沙箱的调用顺序无关。
// 非 Windows 为空操作。
func NoWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}
