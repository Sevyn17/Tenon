package fs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mk(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadRange(t *testing.T) {
	var sb strings.Builder
	for i := 1; i <= 10; i++ {
		sb.WriteString("line")
		sb.WriteString(string(rune('0' + i%10)))
		sb.WriteString("\r\n")
	}
	out, err := readRange("f", []byte(sb.String()), 3, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "line3\nline4\n") || !strings.Contains(out, "offset=5") {
		t.Fatalf("行范围错: %q", out)
	}
	if _, err := readRange("f", []byte("a\n"), 5, 0); err == nil {
		t.Fatal("越界 offset 应报错")
	}
	if _, err := readRange("f", []byte("a\x00b"), 1, 0); err == nil {
		t.Fatal("二进制应报错")
	}
	out, _ = readRange("f", []byte("a\nb\n"), 0, 0)
	if out != "a\nb\n" {
		t.Fatalf("全量读错: %q", out)
	}
}

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		p, n string
		ok   bool
	}{
		{"**/*.go", "a.go", true},
		{"**/*.go", "x/y/a.go", true},
		{"cmd/**/main.go", "cmd/ybase/main.go", true},
		{"cmd/**/main.go", "cmd/main.go", true},
		{"*.go", "x/a.go", false},
		{"x/*.go", "x/y/a.go", false},
	}
	for _, c := range cases {
		if globMatch(c.p, c.n) != c.ok {
			t.Errorf("globMatch(%q,%q) != %v", c.p, c.n, c.ok)
		}
	}
}

func rigTree(t *testing.T) string {
	root := t.TempDir()
	mk(t, root, ".gitignore", "build/\n*.log\n!keep.log\n")
	mk(t, root, "a.go", "package a\nfunc Foo() {}\n")
	mk(t, root, "sub/b.go", "package sub\n// Foo here\nvar x = 1\n")
	mk(t, root, "sub/.gitignore", "gen.go\n")
	mk(t, root, "sub/gen.go", "Foo generated\n")
	mk(t, root, "build/out.go", "Foo built\n")
	mk(t, root, "x.log", "Foo log\n")
	mk(t, root, "keep.log", "Foo kept\n")
	mk(t, root, "node_modules/m.js", "Foo\n")
	mk(t, root, "bin.dat", "Foo\x00\x01")
	return root
}

func TestGrepRespectsIgnore(t *testing.T) {
	root := rigTree(t)
	out, err := grep(context.Background(), root, "Foo", "", "", false, false, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"a.go:2:", "sub/b.go:2:", "keep.log:1:"} {
		if !strings.Contains(out, want) {
			t.Errorf("缺少 %s: %s", want, out)
		}
	}
	for _, bad := range []string{"gen.go", "build/", "x.log", "node_modules", "bin.dat"} {
		if strings.Contains(out, bad) {
			t.Errorf("不应含 %s: %s", bad, out)
		}
	}
	out, _ = grep(context.Background(), root, "foo", "", "*.go", true, false, 0, 0)
	if !strings.Contains(out, "a.go") || strings.Contains(out, "keep.log") {
		t.Fatalf("glob + ignore_case 错: %s", out)
	}
	out, _ = grep(context.Background(), root, "Foo", "sub", "", false, false, 1, 0)
	if !strings.Contains(out, "sub/b.go-1-") || !strings.Contains(out, "sub/b.go-3-") {
		t.Fatalf("上下文错: %s", out)
	}
	out, _ = grep(context.Background(), root, "Foo", "", "", false, false, 0, 1)
	if !strings.Contains(out, "结果不完整") {
		t.Fatalf("limit 未生效: %s", out)
	}
	if _, err := grep(context.Background(), root, "(", "", "", false, false, 0, 0); err == nil {
		t.Fatal("非法正则应报错")
	}
	out, _ = grep(context.Background(), root, "Foo() {", "", "", false, true, 0, 0)
	if !strings.Contains(out, "a.go:2:") {
		t.Fatalf("literal 错: %s", out)
	}
	out, _ = grep(context.Background(), root, "F.o", "", "", false, true, 0, 0)
	if out != "没有匹配" {
		t.Fatalf("literal 不应按正则: %s", out)
	}
	if _, err := grep(context.Background(), root, "x", "..", "", false, false, 0, 0); err == nil {
		t.Fatal("越界路径应报错")
	}
}

func TestGlobFiles(t *testing.T) {
	root := rigTree(t)
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(filepath.Join(root, "a.go"), old, old)
	out, err := globFiles(context.Background(), root, "**/*.go", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 || lines[0] != "sub/b.go" || lines[1] != "a.go" {
		t.Fatalf("glob 结果或排序错: %q", out)
	}
	out, _ = globFiles(context.Background(), root, "*.go", "sub", 0)
	if strings.TrimSpace(out) != "sub/b.go" {
		t.Fatalf("起点目录错: %q", out)
	}
	out, _ = globFiles(context.Background(), root, "*", "", 1)
	if !strings.Contains(out, "已显示最新的 1 个") {
		t.Fatalf("limit 未生效: %q", out)
	}
}
