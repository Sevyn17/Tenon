//go:build !windows

package sandbox

import (
	"fmt"
	"os/exec"
)

// 非 Windows 平台 P0 不支持受限模式（landlock / bwrap 推迟）：不降级，直接报不可用。
func apply(_ *Sandbox, _ *exec.Cmd, mode Mode) (func(), error) {
	return nil, fmt.Errorf("%w：本平台尚不支持 %s", ErrUnavailable, mode)
}

// Reset 非 Windows 平台沙箱不改文件系统，无需恢复。
func Reset(string, []string) error { return nil }
