//go:build windows

package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

// run 在沙箱里用 cmd.exe 写文件，返回是否写成功。
func tryWrite(t *testing.T, s *Sandbox, mode Mode, target string) bool {
	t.Helper()
	cmd := exec.Command("cmd.exe")
	// 原样命令行：Go 的参数转义 cmd.exe 解析不了
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `cmd.exe /c echo x> "` + target + `"`}
	cmd.Dir = s.Root
	cleanup, err := s.Apply(cmd, mode)
	if err != nil {
		t.Fatalf("Apply(%s): %v", mode, err)
	}
	out, _ := cmd.CombinedOutput()
	cleanup()
	_, statErr := os.Stat(target)
	t.Logf("%s → %s: %s", mode, target, out)
	return statErr == nil
}

func TestWorkspaceWrite(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	s := &Sandbox{Root: root, Mode: WorkspaceWrite}
	if !tryWrite(t, s, WorkspaceWrite, filepath.Join(root, "in.txt")) {
		t.Fatal("workspace-write 应能写工作区")
	}
	if tryWrite(t, s, WorkspaceWrite, filepath.Join(outside, "out.txt")) {
		t.Fatal("workspace-write 不应能写工作区外")
	}
	if tryWrite(t, s, WorkspaceWrite, filepath.Join(root, ".ybase", "x.txt")) {
		t.Fatal("workspace-write 不应能写 .ybase")
	}
	if !tryWrite(t, s, Full, filepath.Join(outside, "full.txt")) {
		t.Fatal("danger-full-access 应不受限")
	}
}

// workspace-write 不能写 .git（钩子、配置会在沙箱外以完整权限执行）；没有 .git 的工作区不新建它
// （审查 P0-14：原先只保护 .ybase，沙箱进程能写 .git/hooks 逃出沙箱）。
func TestWorkspaceWriteProtectsGit(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := &Sandbox{Root: root, Mode: WorkspaceWrite}
	if tryWrite(t, s, WorkspaceWrite, filepath.Join(root, ".git", "hooks", "pre-commit")) {
		t.Fatal("workspace-write 不应能写 .git/hooks")
	}
	if tryWrite(t, s, WorkspaceWrite, filepath.Join(root, ".git", "config")) {
		t.Fatal("workspace-write 不应能写 .git/config")
	}
	if !tryWrite(t, s, WorkspaceWrite, filepath.Join(root, "src.txt")) {
		t.Fatal("工作区其他位置仍应可写")
	}
	bare := t.TempDir()
	if !tryWrite(t, &Sandbox{Root: bare, Mode: WorkspaceWrite}, WorkspaceWrite, filepath.Join(bare, "a.txt")) {
		t.Fatal("没有 .git 的工作区应可写")
	}
	if _, err := os.Stat(filepath.Join(bare, ".git")); !os.IsNotExist(err) {
		t.Fatalf("不应新建 .git: %v", err)
	}
	if err := Reset(root, nil); err != nil {
		t.Fatal(err)
	}
}

func TestReadOnly(t *testing.T) {
	root := t.TempDir()
	s := &Sandbox{Root: root, Mode: ReadOnly}
	if tryWrite(t, s, ReadOnly, filepath.Join(root, "ro.txt")) {
		t.Fatal("read-only 不应能写工作区")
	}
	// 读仍可用
	_ = os.WriteFile(filepath.Join(root, "r.txt"), []byte("hello"), 0o644)
	cmd := exec.Command("cmd.exe", "/c", "type r.txt")
	cmd.Dir = root
	cleanup, err := s.Apply(cmd, ReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.CombinedOutput()
	cleanup()
	if err != nil || string(out) != "hello" {
		t.Fatalf("read-only 应能读：%q %v", out, err)
	}
}

func TestReset(t *testing.T) {
	root := t.TempDir()
	s := &Sandbox{Root: root, Mode: WorkspaceWrite}
	if !tryWrite(t, s, WorkspaceWrite, filepath.Join(root, "a.txt")) {
		t.Fatal("workspace-write 应能写工作区")
	}
	if err := Reset(root, nil); err != nil {
		t.Fatal(err)
	}
	if tryWrite(t, &Sandbox{Root: root, Mode: ReadOnly}, ReadOnly, filepath.Join(root, "b.txt")) {
		t.Fatal("read-only 不应能写")
	}
	// Reset 后能力 ACE 已撤销；再次 Apply 会重新授予
	if !tryWrite(t, s, WorkspaceWrite, filepath.Join(root, "c.txt")) {
		t.Fatal("Reset 后重新授予应可写")
	}
}
