//go:build windows

package sandbox

import (
	"path/filepath"
	"testing"
)

// 预授权：Start 时在后台做完工作区授权，第一次受限执行不再承担整棵 ACL 传播的耗时
// （2026-11-04 审查 P1-45：原先同步压在第一次 shell 上，大仓库可能撞工具超时）。
func TestPrepareGrantsWorkspace(t *testing.T) {
	root := t.TempDir()
	s := &Sandbox{Root: root}
	abs, _ := filepath.Abs(root)
	if _, ok := granted.Load(abs); ok {
		t.Fatal("新工作区不应已授权")
	}
	if err := s.Prepare(); err != nil {
		t.Fatalf("预授权: %v", err)
	}
	if _, ok := granted.Load(abs); !ok {
		t.Fatal("预授权后工作区应记为已授权")
	}
	if !tryWrite(t, s, WorkspaceWrite, filepath.Join(root, "after.txt")) {
		t.Fatal("预授权后 workspace-write 应能写工作区")
	}
	if err := s.Prepare(); err != nil { // 幂等
		t.Fatalf("重复预授权: %v", err)
	}
}
