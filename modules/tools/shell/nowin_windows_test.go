//go:build windows

package shell

import (
	"context"
	"syscall"
	"testing"
)

// shell 工具起的子进程不弹控制台窗口：CREATE_NO_WINDOW + HideWindow，且保留进程组 flag。
func TestShellCommandNoWindow(t *testing.T) {
	s := NewShell(t.TempDir())
	cmd := s.command(context.Background(), "echo ok", &tailBuf{})
	a := cmd.SysProcAttr
	if a == nil || !a.HideWindow || a.CreationFlags&0x08000000 == 0 || a.CreationFlags&syscall.CREATE_NEW_PROCESS_GROUP == 0 {
		t.Fatalf("SysProcAttr=%+v", a)
	}
}
