package hooks

import "testing"

// 穿插项 13：用户级 hooks.toml 的目录认 TENON_HOME（原先固定 ~/.tenon，与权限、技能组件不一致）。
func TestUserHomeHonorsTenonHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TENON_HOME", dir)
	if got := userHome(); got != dir {
		t.Fatalf("userHome() = %q，应为 TENON_HOME %q", got, dir)
	}
	t.Setenv("TENON_HOME", "")
	if got := userHome(); got == "" || got == dir {
		t.Fatalf("没设 TENON_HOME 时应落到 ~/.tenon，得 %q", got)
	}
}
