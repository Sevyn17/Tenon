package toolhub

// M2/M3 回归：「按型取值」的判断收进 Item 自身（2026-11-12）。
//
// 原来调用方到处写 `it.Type == ItemLevel` 来选字段（eval.go 里那个三重取反条件），
// 与 Validate() 的 4 条「不应带 / 必须提供」检查同源：一个 Type 决定哪些字段有意义。
// 现在调用方只问语义（NeedsLevel / DefaultLevel），不再问类型。

import (
	"testing"
)

// NeedsLevel：只有 level 型要「级别」，其它型要「动作」。
func TestItemNeedsLevel(t *testing.T) {
	cases := []struct {
		typ  ItemType
		want bool
	}{
		{ItemSwitch, false},
		{ItemMatch, false},
		{ItemLevel, true},
	}
	for _, c := range cases {
		it := Item{Type: c.typ}
		if got := it.NeedsLevel(); got != c.want {
			t.Errorf("Type=%s NeedsLevel()=%v，期望 %v", c.typ, got, c.want)
		}
	}
}

// DefaultLevel：level 型按「档位建议 → 第一个级别」回落；其它型返回空。
func TestItemDefaultLevel(t *testing.T) {
	lv := Item{Type: ItemLevel, Levels: []string{"a", "b"},
		SuggestLevel: map[string]string{"workspace": "b"}}
	if got := lv.DefaultLevel("workspace"); got != "b" {
		t.Errorf("有建议时应取建议值，得到 %q", got)
	}
	if got := lv.DefaultLevel("full"); got != "a" {
		t.Errorf("无建议时应取第一个级别，得到 %q", got)
	}
	// 非 level 型：没有「级别」概念
	sw := Item{Type: ItemSwitch}
	if got := sw.DefaultLevel("workspace"); got != "" {
		t.Errorf("switch 型不该有级别，得到 %q", got)
	}
	// level 型但没声明 Levels：返回空（Validate 会拦住这种，这里只是兜底）
	if got := (Item{Type: ItemLevel}).DefaultLevel("workspace"); got != "" {
		t.Errorf("无 Levels 时应返回空，得到 %q", got)
	}
}
