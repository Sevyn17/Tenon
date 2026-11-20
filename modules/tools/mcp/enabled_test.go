package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 启用记录：都没写 = 启用；工作区覆盖优先于全局；与全局结果相同时不留覆盖。
func TestEnabledRecord(t *testing.T) {
	p := filepath.Join(t.TempDir(), EnabledFile)
	a, b := &Enabled{Path: p, Workspace: "a"}, &Enabled{Path: p, Workspace: "b"}
	st, err := a.Statuses([]string{"x"})
	if err != nil || !st["x"].Enabled {
		t.Fatalf("没有记录应启用：%+v %v", st, err)
	}
	if err := a.Set("x", false, EnableGlobal); err != nil {
		t.Fatal(err)
	}
	if err := a.Set("x", true, EnableWorkspace); err != nil {
		t.Fatal(err)
	}
	sa, _ := a.Statuses([]string{"x"})
	sb, _ := b.Statuses([]string{"x"})
	if !sa["x"].Enabled || sb["x"].Enabled || sb["x"].Scope != EnableGlobal {
		t.Fatalf("a=%+v b=%+v", sa, sb)
	}
	if err := b.Set("x", false, EnableWorkspace); err != nil { // 与全局相同：不记覆盖
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if strings.Contains(string(raw), "'b'") || strings.Contains(string(raw), "\"b\"") {
		t.Fatalf("与全局相同不应留覆盖：\n%s", raw)
	}
	if err := (&Enabled{Path: p}).Set("x", true, EnableWorkspace); err == nil {
		t.Fatal("没有工作区不能按工作区记录")
	}
	if err := a.Set("x", true, "bogus"); err == nil {
		t.Fatal("非法 scope 应报错")
	}
	var nilE *Enabled
	if st, _ := nilE.Statuses([]string{"x"}); !st["x"].Enabled {
		t.Fatal("nil 记录应全部启用")
	}
}
