package session

import (
	"encoding/json"
	"testing"
)

// roundTrip 把当前内核快照经 JSON 往返后 Restore（模拟进程重启）。
func roundTrip(t *testing.T, c *Core) *Core {
	t.Helper()
	b, err := json.Marshal(c.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	var s Snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	r, err := Restore(s)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	return r
}

func TestResumedWaitingModelKeepsRequestID(t *testing.T) {
	h := newHarness(t, 8)
	n1, _ := h.core.Input(txt("问"), nil)
	r := roundTrip(t, h.core)
	need := r.Resumed()
	if need == nil || need.Kind != NeedModel || need.Model.RequestID != n1.Model.RequestID {
		t.Fatalf("应沿用 request_id: %+v", need)
	}
	if need.ID == n1.ID {
		t.Fatalf("need_id 应为新值")
	}
	if n, err := r.ModelDone(need.Model.RequestID, need.ID, AssistantItem{Content: txt("答")}, StopReason{}); err != nil || n.Kind != NeedDone {
		t.Fatalf("恢复后的需求应可被满足: %v %+v", err, n)
	}
	// 旧 need_id 被拒（S2）。
	r2 := roundTrip(t, h.core)
	_ = r2.Resumed()
	if _, err := r2.ModelDone(n1.Model.RequestID, n1.ID, AssistantItem{Content: txt("答")}, StopReason{}); err == nil {
		t.Fatalf("旧 need_id 应拒收")
	}
}

func TestResumedWaitingToolsKeepsCallIDs(t *testing.T) {
	h := newHarness(t, 8)
	n1, _ := h.core.Input(txt("问"), nil)
	calls := []ToolCall{{Name: "echo", Args: json.RawMessage(`{}`)}, {Name: "nope", Args: json.RawMessage(`{}`)}}
	n2, _ := h.core.ModelDone(n1.Model.RequestID, n1.ID, AssistantItem{ToolCalls: calls}, StopReason{})
	_, _ = h.core.SubmitResult(n2.Tools.Calls[0].ID, txt("r"), ToolOK)
	r := roundTrip(t, h.core)
	need := r.Resumed()
	if need == nil || need.Kind != NeedTools || len(need.Tools.Calls) != 1 || need.Tools.Calls[0].ID != n2.Tools.Calls[1].ID {
		t.Fatalf("应只剩第二个调用、id 不变: %+v", need)
	}
	if need.Tools.Hints[need.Tools.Calls[0].ID] == "" {
		t.Fatalf("无效调用的提示应重算")
	}
}

func TestResumedIdleNil(t *testing.T) {
	h := newHarness(t, 8)
	if roundTrip(t, h.core).Resumed() != nil {
		t.Fatal("Idle 应无需求")
	}
}
