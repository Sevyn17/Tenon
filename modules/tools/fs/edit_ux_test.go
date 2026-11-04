package fs

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// replace_all 替换全部精确出现（2026-11-04 审查 P2 工具体验：原先 old_text 必须唯一，改名要逐处加上下文）。
func TestEditReplaceAll(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.go")
	writeT(t, p, "foo()\nbar\nfoo()\nfoo()\n")
	msg, err := applyEdits(p, "a.go", []editItem{{Old: "foo", New: "baz", All: true}})
	if err != nil {
		t.Fatal(err)
	}
	if got := readT(t, p); got != "baz()\nbar\nbaz()\nbaz()\n" {
		t.Fatalf("内容不符: %q", got)
	}
	if !strings.Contains(msg, "3 处") {
		t.Fatalf("应报 3 处: %s", msg)
	}
	if _, err := applyEdits(p, "a.go", []editItem{{Old: "nope", New: "x", All: true}}); err == nil {
		t.Fatal("replace_all 找不到时应报错")
	}
}

// 结果按位置列出各处（原先 sort.Strings 让"第 10 处"排在"第 2 处"前），并回显改动后的片段（带行号）。
func TestEditNotesOrderAndSnippet(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.txt")
	var src strings.Builder
	var edits []editItem
	for i := 1; i <= 11; i++ {
		fmt.Fprintf(&src, "line%02d\n", i)
		edits = append(edits, editItem{Old: fmt.Sprintf("line%02d", i), New: fmt.Sprintf("L%02d", i)})
	}
	writeT(t, p, src.String())
	msg, err := applyEdits(p, "a.txt", edits)
	if err != nil {
		t.Fatal(err)
	}
	if i2, i10 := strings.Index(msg, "第 2 处"), strings.Index(msg, "第 10 处"); i2 < 0 || i10 < 0 || i2 > i10 {
		t.Fatalf("各处应按位置排列：\n%s", msg)
	}
	if !strings.Contains(msg, "1\tL01") || !strings.Contains(msg, "2\tL02") {
		t.Fatalf("应回显带行号的改动片段：\n%s", msg)
	}
}
