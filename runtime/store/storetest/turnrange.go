package storetest

import (
	"context"
	"testing"

	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
)

// testTurnRange 可选的 TurnRangeStore（客户端改进 M8 协议T15）：每个回合的 [from, to) 与日志一致；
// 不存在的回合、未知会话 ok = false；回退截掉的回合不再能找到。
func testTurnRange(t *testing.T, st store.Store) {
	tr, ok := st.(store.TurnRangeStore)
	if !ok {
		t.Skip("存储未实现可选的 TurnRangeStore")
	}
	ctx := context.Background()
	c := newCore()
	create(t, st, store.SessionMeta{ID: "tr"}, c)
	for i, text := range []string{"一", "二", "三", "四"} {
		// 偶数位是一问一答（2 条），奇数位是只记录的零步回合（1 条），让各回合长度不同。
		if i%2 == 1 {
			if err := c.Record(txt(text), nil); err != nil {
				t.Fatal(err)
			}
			continue
		}
		n, err := c.Input(txt(text), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.ModelDone(n.Model.RequestID, n.ID, session.AssistantItem{Content: txt("re:" + text)}, session.StopReason{V: "stop"}); err != nil {
			t.Fatal(err)
		}
	}
	n := commitAll(t, st, "tr", c, 0, nil)
	want := func() map[uint32][2]int {
		m := map[uint32][2]int{}
		for i, e := range c.Snapshot().Entries {
			r, seen := m[e.Turn]
			if !seen {
				r[0] = i
			}
			r[1] = i + 1
			m[e.Turn] = r
		}
		return m
	}
	check := func(why string) {
		t.Helper()
		w := want()
		for turn := uint32(0); turn <= 6; turn++ {
			from, to, ok, err := tr.TurnRange(ctx, "tr", turn)
			if err != nil {
				t.Fatal(err)
			}
			r, has := w[turn]
			if ok != has || (has && (from != r[0] || to != r[1])) {
				t.Fatalf("%s：回合 %d 得到 [%d,%d) ok=%v，期望 %v ok=%v", why, turn, from, to, ok, r, has)
			}
		}
	}
	check("写入后")
	if _, _, ok, err := tr.TurnRange(ctx, "nope", 1); err != nil || ok {
		t.Fatalf("未知会话应 ok=false：%v %v", ok, err)
	}
	// 回退到第 3 回合起点：第 3、4 回合不再存在。
	to := want()[3][0]
	if err := c.Rewind(to); err != nil {
		t.Fatal(err)
	}
	s := c.Snapshot()
	if err := st.Commit(ctx, store.Checkpoint{SessionID: "tr", ExpectLogLen: n, TruncateFrom: &to, AppendFrom: to,
		Header: s.Header, RunState: "idle"}); err != nil {
		t.Fatal(err)
	}
	check("回退后")
	if _, _, ok, _ := tr.TurnRange(ctx, "tr", 3); ok {
		t.Fatal("回退截掉的回合不应再找到")
	}
}
