package storetest

import (
	"context"
	"testing"

	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
)

// testEntryLocate 可选的 EntryLocator（Mortise docs/72 B2，session.locate）：当前日志里的条目报所在回合、rewound=false；
// 回退后移进归档的条目仍能找到、rewound=true；未知条目、未知会话 ok=false。
func testEntryLocate(t *testing.T, st store.Store) {
	el, ok := st.(store.EntryLocator)
	if !ok {
		t.Skip("存储未实现可选的 EntryLocator")
	}
	ctx := context.Background()
	c := newCore()
	create(t, st, store.SessionMeta{ID: "el"}, c)
	for _, text := range []string{"一", "二", "三"} {
		n, err := c.Input(txt(text), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.ModelDone(n.Model.RequestID, n.ID, session.AssistantItem{Content: txt("re:" + text)}, session.StopReason{V: "stop"}); err != nil {
			t.Fatal(err)
		}
	}
	n := commitAll(t, st, "el", c, 0, nil)
	es := c.Snapshot().Entries
	for _, e := range es {
		turn, rewound, ok, err := el.EntryTurn(ctx, "el", e.ID)
		if err != nil || !ok || rewound || turn != e.Turn {
			t.Fatalf("条目 %s：turn=%d rewound=%v ok=%v err=%v，期望回合 %d", e.ID, turn, rewound, ok, err, e.Turn)
		}
	}
	if _, _, ok, err := el.EntryTurn(ctx, "el", "nope"); err != nil || ok {
		t.Fatalf("未知条目应 ok=false：%v %v", ok, err)
	}
	if _, _, ok, err := el.EntryTurn(ctx, "missing", es[0].ID); err != nil || ok {
		t.Fatalf("未知会话应 ok=false：%v %v", ok, err)
	}
	// 回退到第 3 回合起点：第 3 回合的条目进归档，仍能找到，rewound=true。
	to := -1
	var gone []*session.Entry
	for i, e := range es {
		if e.Turn == 3 {
			if to < 0 {
				to = i
			}
			gone = append(gone, e)
		}
	}
	if to < 0 || len(gone) == 0 {
		t.Fatal("没有第 3 回合")
	}
	if err := c.Rewind(to); err != nil {
		t.Fatal(err)
	}
	s := c.Snapshot()
	if err := st.Commit(ctx, store.Checkpoint{SessionID: "el", ExpectLogLen: n, TruncateFrom: &to, AppendFrom: to,
		Header: s.Header, RunState: "idle"}); err != nil {
		t.Fatal(err)
	}
	for _, e := range gone {
		turn, rewound, ok, err := el.EntryTurn(ctx, "el", e.ID)
		if err != nil || !ok || !rewound || turn != 3 {
			t.Fatalf("已回退条目 %s：turn=%d rewound=%v ok=%v err=%v", e.ID, turn, rewound, ok, err)
		}
	}
	if turn, rewound, ok, _ := el.EntryTurn(ctx, "el", es[0].ID); !ok || rewound || turn != es[0].Turn {
		t.Fatalf("回退后仍在日志里的条目：turn=%d rewound=%v ok=%v", turn, rewound, ok)
	}
}
