package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ProjectRoot 从 dir 向上找 .git；找不到返回 dir 本身。
func TestProjectRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := ProjectRoot(deep); err != nil || got != root {
		t.Fatalf("应从子目录找到项目根：%q, %v（期望 %q）", got, err, root)
	}
	if got, err := ProjectRoot(root); err != nil || got != root {
		t.Fatalf("根自己应返回自己：%q, %v", got, err)
	}

	// 没有 .git：一路向上找不到，返回传入的 dir。
	plain := t.TempDir()
	plainDeep := filepath.Join(plain, "x", "y")
	if err := os.MkdirAll(plainDeep, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := ProjectRoot(plainDeep); err != nil || got != plainDeep {
		t.Fatalf("找不到 .git 应原样返回：%q, %v（期望 %q）", got, err, plainDeep)
	}
}

// .git 是文件（worktree / submodule 的写法）也算项目根。
func TestProjectRootGitFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := ProjectRoot(sub); err != nil || got != root {
		t.Fatalf(".git 是文件时也算项目根：%q, %v（期望 %q）", got, err, root)
	}
}

// ClipUTF8 截到至多 n 字节，且不切断 UTF-8 字符。
func TestClipUTF8(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"hello", 5, "hello"},
		{"hello", 3, "hel"},
		{"hello", 0, ""},
		{"hello", -1, ""},
		{"hello", 99, "hello"},
		{"中文测试", 6, "中文"},  // 每字 3 字节
		{"中文测试", 7, "中文"},  // 第 7 字节落在「测」中间 → 退到 6
		{"中文测试", 8, "中文"},  // 第 8 字节仍在「测」中间
		{"中文测试", 9, "中文测"}, // 正好一整字
		{"a中", 1, "a"},     // 「中」占 3 字节，1 字节只留 a
		{"a中", 2, "a"},     // 落在「中」内部 → 退到 a
		{"a中", 3, "a"},     // 仍在「中」内部（中占第 2–4 字节）
		{"a中", 4, "a中"},
		{"", 5, ""},
	}
	for _, c := range cases {
		if got := ClipUTF8(c.in, c.n); got != c.want {
			t.Errorf("ClipUTF8(%q, %d) = %q，期望 %q", c.in, c.n, got, c.want)
		}
	}
	// 结果必须是合法 UTF-8 前缀（不含半个字符）。
	for _, s := range []string{"中文测试", "🙂🙂", "aé中"} {
		for n := 0; n <= len(s); n++ {
			got := ClipUTF8(s, n)
			if !strings.HasPrefix(s, got) {
				t.Fatalf("ClipUTF8(%q, %d) = %q 不是原串前缀", s, n, got)
			}
			if len(got) > n {
				t.Fatalf("ClipUTF8(%q, %d) 超长：%q", s, n, got)
			}
		}
	}
}
