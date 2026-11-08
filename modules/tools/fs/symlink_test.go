package fs

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// link 在工作目录里建一个指向 target 的目录链接：Unix 用符号链接，Windows 用 junction
// （普通用户就能建，克隆来的仓库可以直接带一个）。建不了就跳过。
func link(t *testing.T, at, target string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		if out, err := exec.Command("cmd", "/c", "mklink", "/J", at, target).CombinedOutput(); err != nil {
			t.Skipf("建不了 junction：%v %s", err, out)
		}
		return
	}
	if err := os.Symlink(target, at); err != nil {
		t.Skipf("建不了符号链接：%v", err)
	}
}

// B6：工作目录里的链接指向外面时，文件工具不能经它读写工作目录之外的文件。
func TestFileToolsRejectLinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("TOP SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	link(t, filepath.Join(root, "lnk"), outside)

	if o := callFileTool(t, WriteFile(root), `{"path":"lnk/pwned.txt","content":"x"}`); o.Status != session.ToolError {
		t.Fatalf("经链接写到工作目录外应失败：%s", outText(o))
	}
	if _, err := os.Stat(filepath.Join(outside, "pwned.txt")); err == nil {
		t.Fatal("文件不应写到工作目录外")
	}
	if o := callFileTool(t, ListDir(root), `{"path":"lnk"}`); o.Status != session.ToolError {
		t.Fatalf("经链接列工作目录外的目录应失败：%s", outText(o))
	}
	for _, tl := range fileReaders(root) {
		o := callFileTool(t, tl, `{"path":"lnk/secret.txt"}`)
		if strings.Contains(outText(o), "TOP SECRET") {
			t.Fatalf("%s 不应经链接读到工作目录外的文件", tl.Spec.Name)
		}
	}
	// 工作目录内正常的子目录与新文件不受影响。
	if o := callFileTool(t, WriteFile(root), `{"path":"sub/new.txt","content":"ok"}`); o.Status != session.ToolOK {
		t.Fatalf("工作目录内写新文件应成功：%s", outText(o))
	}
}

// fileReaders 按 path 参数读文件内容的工具。
func fileReaders(root string) []*toolhub.Tool {
	return []*toolhub.Tool{ReadFile(root), Grep(root), Edit(root)}
}

// 工作目录本身经链接打开（常见：用户把项目放在链接目录下）时，目录内的操作照常。
func TestFileToolsRootBehindLink(t *testing.T) {
	real := t.TempDir()
	parent := t.TempDir()
	root := filepath.Join(parent, "proj")
	link(t, root, real)
	if o := callFileTool(t, WriteFile(root), `{"path":"a.txt","content":"ok"}`); o.Status != session.ToolOK {
		t.Fatalf("工作目录经链接打开时应可写：%s", outText(o))
	}
	if b, err := os.ReadFile(filepath.Join(real, "a.txt")); err != nil || string(b) != "ok" {
		t.Fatalf("应写进真实目录：%q %v", b, err)
	}
}
