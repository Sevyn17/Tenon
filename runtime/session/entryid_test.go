package session

import "testing"

// protocol 2 流式关联（06 10.5）：assistant 条目的 id 就是本步的请求 id，流式事件在条目落库前就能带上 entry_id。
func TestAssistantEntryIDIsRequestID(t *testing.T) {
	h := newHarness(t, 0)
	n, err := h.core.Input(txt("你好"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := h.core.PendingEntryID(); got != n.Model.RequestID {
		t.Fatalf("等模型时 PendingEntryID 应为请求 id %s：%s", n.Model.RequestID, got)
	}
	if _, err := h.core.ModelDone(n.Model.RequestID, n.ID, AssistantItem{Content: txt("答")}, StopReason{}); err != nil {
		t.Fatal(err)
	}
	es := h.core.Snapshot().Entries
	if a := es[len(es)-1]; a.Item.Kind != KindAssistant || a.ID != n.Model.RequestID {
		t.Fatalf("assistant 条目 id 应为请求 id %s：%s", n.Model.RequestID, a.ID)
	}
	if h.core.PendingEntryID() != "" {
		t.Fatal("空闲时 PendingEntryID 应为空")
	}
	// 中断写的 interrupted 条目同样用请求 id。
	n2, _ := h.core.Input(txt("再来"), nil)
	if _, err := h.core.ModelInterrupted(n2.Model.RequestID, n2.ID, AssistantItem{Content: txt("半")}, CancelledReason()); err != nil {
		t.Fatal(err)
	}
	for _, e := range h.core.Snapshot().Entries {
		if e.Item.Kind == KindAssistant && e.Turn == 2 && e.ID != n2.Model.RequestID {
			t.Fatalf("interrupted 条目 id 应为请求 id：%s", e.ID)
		}
	}
}
