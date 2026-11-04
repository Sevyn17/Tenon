package fs

import (
	"context"
	"strings"
	"testing"
)

// grep / glob 跳过 vendor、node_modules 时在结果里说明（2026-11-04 审查 P2 工具体验：原先静默跳过）。
func TestSearchNotesSkippedDeps(t *testing.T) {
	root := t.TempDir()
	mk(t, root, "a.go", "needle\n")
	mk(t, root, "vendor/x/b.go", "needle\n")
	mk(t, root, ".git/c", "needle\n")
	out, err := grep(context.Background(), root, "needle", "", "", false, false, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "未搜索依赖目录 vendor") || strings.Contains(out, ".git") || strings.Contains(out, "vendor/x/b.go:") {
		t.Fatalf("grep 应提示跳过 vendor、不提 .git：%s", out)
	}
	out, err = globFiles(context.Background(), root, "**/*.go", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "未搜索依赖目录 vendor") {
		t.Fatalf("glob 应提示跳过 vendor：%s", out)
	}
}
