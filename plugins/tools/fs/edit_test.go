package fs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeT(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readT(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// M5.1：多处替换在原文件上匹配、原子写入；保留 BOM 与 CRLF。
func TestEditMultiAndFidelity(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.txt")
	writeT(t, p, "\xEF\xBB\xBFone\r\ntwo\r\nthree\r\n")
	msg, err := applyEdits(p, "a.txt", []editItem{{Old: "one", New: "1"}, {Old: "three", New: "3\n3b"}})
	if err != nil {
		t.Fatalf("applyEdits: %v", err)
	}
	if got := readT(t, p); got != "\xEF\xBB\xBF1\r\ntwo\r\n3\r\n3b\r\n" {
		t.Fatalf("内容不符: %q", got)
	}
	if !strings.Contains(msg, "2 处") {
		t.Fatalf("结果应报处数: %s", msg)
	}
}

func TestEditFailuresWriteNothing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.txt")
	orig := "x\nx\ny\n"
	writeT(t, p, orig)
	cases := []struct {
		name  string
		edits []editItem
		want  string
	}{
		{"多处命中", []editItem{{Old: "x", New: "z"}}, "命中 2 处"},
		{"未命中", []editItem{{Old: "nope", New: "z"}}, "找不到"},
		{"一处失败整体不写", []editItem{{Old: "y", New: "Y"}, {Old: "nope", New: "z"}}, "第 2 处"},
		{"重叠", []editItem{{Old: "x\ny", New: "a"}, {Old: "y\n", New: "b"}}, "重叠"},
		{"无变化", []editItem{{Old: "y", New: "y"}}, "没有变化"},
		{"已存在不能新建", []editItem{{Old: "", New: "q"}}, "已存在"},
	}
	for _, c := range cases {
		_, err := applyEdits(p, "a.txt", c.edits)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: 期望含 %q，得 %v", c.name, c.want, err)
		}
		if got := readT(t, p); got != orig {
			t.Fatalf("%s: 失败时不应写入: %q", c.name, got)
		}
	}
}

func TestEditFuzzyAndCreate(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.go")
	writeT(t, p, "keep  \nsay(\u201chi\u201d)   \nkeep2\n")
	msg, err := applyEdits(p, "a.go", []editItem{{Old: "say(\"hi\")", New: "say(\"bye\")"}})
	if err != nil {
		t.Fatalf("规范化匹配应命中: %v", err)
	}
	if !strings.Contains(msg, "规范化匹配") {
		t.Fatalf("应注明规范化匹配: %s", msg)
	}
	if got := readT(t, p); got != "keep  \nsay(\"bye\")\nkeep2\n" {
		t.Fatalf("只应改命中的行，其余字节保留: %q", got)
	}
	np := filepath.Join(dir, "sub", "new.txt")
	if _, err := applyEdits(np, "sub/new.txt", []editItem{{Old: "", New: "hello\n"}}); err != nil {
		t.Fatalf("新建: %v", err)
	}
	if readT(t, np) != "hello\n" {
		t.Fatal("新建内容不符")
	}
}
