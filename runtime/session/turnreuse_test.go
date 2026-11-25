package session

import "testing"

// 回退后新回合号重用（Mortise docs/76 §16 D4）：新回合号 = 最后一条条目的回合号 + 1，
// 回退截掉尾部后，下一个回合拿到的是被截掉那个回合的号。读取工具因此按条目 id 定位，回合号只用来显示。
func TestRewindReusesTurnNumber(t *testing.T) {
	h := newHarness(t, 8)
	for _, q := range []string{"u1", "u2"} {
		n, err := h.core.Input(txt(q), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.core.ModelDone(n.Model.RequestID, n.ID, AssistantItem{Content: txt("re:" + q)}, StopReason{}); err != nil {
			t.Fatal(err)
		}
	}
	old := h.core.items[2]
	if old.Turn != 2 {
		t.Fatalf("第二回合首条回合号应为 2：%d", old.Turn)
	}
	if err := h.core.Rewind(2); err != nil {
		t.Fatal(err)
	}
	if _, err := h.core.Input(txt("u3"), nil); err != nil {
		t.Fatal(err)
	}
	e := h.core.items[len(h.core.items)-1]
	if e.Turn != 2 {
		t.Fatalf("回退后的新回合应重用回合号 2，得到 %d", e.Turn)
	}
	if e.ID == old.ID {
		t.Fatal("回合号重用但条目 id 不能重用")
	}
}
