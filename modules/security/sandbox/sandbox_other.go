//go:build !windows

package sandbox

import (
	"fmt"
	"os/exec"
)

// platformSupported 本平台有没有受限模式的实现（landlock / bwrap 推迟）。变量而非常量：测试可切换。
var platformSupported = false

// 非 Windows 平台 P0 不支持受限模式：Sandbox.Degrade 为假时不降级，直接报不可用。
func apply(_ *Sandbox, _ *exec.Cmd, mode Mode) (func(), error) {
	return nil, fmt.Errorf("%w：本平台尚不支持 %s", ErrUnavailable, mode)
}

// Reset 非 Windows 平台沙箱不改文件系统，无需恢复。
func Reset(string, []string) error { return nil }
