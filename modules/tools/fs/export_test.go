package fs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Sevyn17/tenon/runtime/toolhub"
)

func TestResolveExported(t *testing.T) {
	root := t.TempDir()
	if p, err := Resolve(root, "a/b.txt"); err != nil || p != filepath.Join(root, "a", "b.txt") {
		t.Fatalf("相对路径：%q %v", p, err)
	}
	for _, bad := range []string{"../x", filepath.Join(filepath.Dir(root), "other")} {
		if _, err := Resolve(root, bad); err == nil {
			t.Fatalf("%q 应被拒绝", bad)
		}
	}
	out := t.TempDir()
	if err := os.Symlink(out, filepath.Join(root, "link")); err == nil {
		if _, err := Resolve(root, "link/x.txt"); err == nil {
			t.Fatal("经符号链接越界应被拒绝")
		}
	}
}

// Windows 上 C:\Proj\a 与 c:\proj\A 视为同一路径（spike 第八节）。
func TestWithinCaseInsensitiveWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("只在 Windows 上不区分大小写")
	}
	if !Within(`C:\Proj`, `c:\proj\A`) {
		t.Fatal(`C:\Proj 应包含 c:\proj\A`)
	}
}

func TestWriteAtomicAndWalk(t *testing.T) {
	root := t.TempDir()
	if err := WriteAtomic(filepath.Join(root, "d", "a.txt"), []byte("hi")); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, ".gitignore"), []byte("ign.txt\n"), 0o644)
	os.WriteFile(filepath.Join(root, "ign.txt"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(root, "node_modules"), 0o755)
	os.WriteFile(filepath.Join(root, "node_modules", "m.js"), []byte("x"), 0o644)
	m, err := Walk(context.Background(), root, 0)
	if err != nil {
		t.Fatal(err)
	}
	if st, ok := m["d/a.txt"]; !ok || st.Size != 2 {
		t.Fatalf("应含 d/a.txt：%v", m)
	}
	if _, ok := m["ign.txt"]; ok {
		t.Fatal("忽略的文件不应出现")
	}
	if _, ok := m["node_modules/m.js"]; ok {
		t.Fatal("node_modules 不应出现")
	}
	if _, err := Walk(context.Background(), root, 1); !errors.Is(err, ErrTooManyFiles) {
		t.Fatalf("超上限应报 ErrTooManyFiles：%v", err)
	}
}

// write_file / edit 声明 write_path（检查点据此精确快照）。
func TestWriteToolsDeclareWritePath(t *testing.T) {
	root := t.TempDir()
	for _, tl := range []*toolhub.Tool{WriteFile(root), Edit(root)} {
		if tl.Attrs[toolhub.AttrWritePath] != "path" {
			t.Fatalf("%s 应声明 write_path=path", tl.Spec.Name)
		}
	}
}
