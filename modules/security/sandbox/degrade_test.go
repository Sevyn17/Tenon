package sandbox

import (
	"os/exec"
	"testing"
)

// 平台没有沙箱实现时：Degrade 的沙箱按不受限执行（插件装配时已告警），不 Degrade 的报不可用。
// 平台能力只由本包判断，权限插件不再写死"非 Windows 一律 full access"（审查 P1-14）。
func TestDegradeWhenPlatformUnsupported(t *testing.T) {
	old := platformSupported
	platformSupported = false
	defer func() { platformSupported = old }()
	s := &Sandbox{Root: t.TempDir(), Degrade: true}
	cleanup, err := s.Apply(exec.Command("x"), WorkspaceWrite)
	if err != nil || cleanup == nil {
		t.Fatalf("Degrade 时应不受限执行: %v", err)
	}
	cleanup()
	if PlatformSupported() {
		t.Fatal("PlatformSupported 应报告不支持")
	}
}
