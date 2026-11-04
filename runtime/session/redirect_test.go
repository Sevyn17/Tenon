package session

// Redirect 边界测试（session-core C3 ③）：T6 / T7 / T11 的组合情形。

import (
	"encoding/json"
	"testing"
)

func lastN(h *harness, n int) []*Entry { return h.core.items[len(h.core.items)-n:] }

// ① WaitingModel 已改向，回复的调用全部无效：调用补 Redirected（不是校验错误），进入下一步。
func TestRedirect_AllInvalidCalls(t *testing.T) {
	h := newHarness(t, 8)
	n1, _ := h.core.Input(txt("问"), nil)
	if _, err := h.core.Steer(txt("改向"), nil, SteerRedirect); err != nil {
		t.Fatal(err)
	}
	calls := []ToolCall{{Name: "nope", Args: json.RawMessage(`{}`)}}
	need, err := h.core.ModelDone(n1.Model.RequestID, n1.ID, AssistantItem{ToolCalls: calls}, StopReason{})
	if err != nil {
		t.Fatal(err)
	}
	if need.Kind != NeedModel || h.core.hdr.Redirected {
		t.Fatalf("应进入下一步并清除改向标记: %+v", need)
	}
	es := lastN(h, 2)
	if es[0].Item.Kind != KindToolResult || es[0].Item.ToolResult.Status != ToolRedirected {
		t.Fatalf("无效调用也应以 Redirected 闭合: %+v", es[0].Item)
	}
	if es[1].Item.Kind != KindUser || es[1].Item.User.Content[0].Text != "改向" {
		t.Fatalf("改向输入应在边界追加: %+v", es[1].Item)
	}
}

// ② WaitingModel 已改向，回复无调用：正文保留，改向输入追加，进入下一步（不是 Done）。
func TestRedirect_NoCalls(t *testing.T) {
	h := newHarness(t, 8)
	n1, _ := h.core.Input(txt("问"), nil)
	_, _ = h.core.Steer(txt("改向"), nil, SteerRedirect)
	need, err := h.core.ModelDone(n1.Model.RequestID, n1.ID, AssistantItem{Content: txt("半截答")}, StopReason{})
	if err != nil {
		t.Fatal(err)
	}
	if need.Kind != NeedModel {
		t.Fatalf("有暂存插话应进入下一步: %+v", need)
	}
	es := lastN(h, 2)
	if es[0].Item.Kind != KindAssistant || es[1].Item.Kind != KindUser {
		t.Fatalf("顺序应为 Assistant → 改向输入: %v %v", es[0].Item.Kind, es[1].Item.Kind)
	}
}

// ③ 改向前后各有一条 Append 插话：三条按到达顺序在边界追加。
func TestRedirect_WithAppendSteers(t *testing.T) {
	h := newHarness(t, 8)
	n1, _ := h.core.Input(txt("问"), nil)
	_, _ = h.core.Steer(txt("a"), nil, SteerAppend)
	_, _ = h.core.Steer(txt("r"), nil, SteerRedirect)
	_, _ = h.core.Steer(txt("b"), nil, SteerAppend)
	calls := []ToolCall{{Name: "echo", Args: json.RawMessage(`{}`)}}
	need, err := h.core.ModelDone(n1.Model.RequestID, n1.ID, AssistantItem{ToolCalls: calls}, StopReason{})
	if err != nil || need.Kind != NeedModel {
		t.Fatalf("应进入下一步: %v %+v", err, need)
	}
	es := lastN(h, 3)
	for i, want := range []string{"a", "r", "b"} {
		if es[i].Item.Kind != KindUser || es[i].Item.User.Content[0].Text != want {
			t.Fatalf("第 %d 条应为 %q: %+v", i, want, es[i].Item)
		}
	}
}

// ④ WaitingTools 部分结果已到再改向：已到结果保留，剩余调用补 Redirected，立即下一步。
func TestRedirect_PartialResults(t *testing.T) {
	h := newHarness(t, 8)
	n1, _ := h.core.Input(txt("问"), nil)
	calls := []ToolCall{{Name: "echo", Args: json.RawMessage(`{"a":1}`)}, {Name: "echo", Args: json.RawMessage(`{"b":2}`)}}
	n2, _ := h.core.ModelDone(n1.Model.RequestID, n1.ID, AssistantItem{ToolCalls: calls}, StopReason{})
	if _, err := h.core.SubmitResult(n2.Tools.Calls[0].ID, txt("r1"), ToolOK); err != nil {
		t.Fatal(err)
	}
	need, err := h.core.Steer(txt("别查了"), nil, SteerRedirect)
	if err != nil || need == nil || need.Kind != NeedModel {
		t.Fatalf("T7 应立即进入下一步: %v %+v", err, need)
	}
	es := lastN(h, 3)
	if es[0].Item.ToolResult.Status != ToolOK || es[1].Item.ToolResult.Status != ToolRedirected || es[2].Item.Kind != KindUser {
		t.Fatalf("应为 OK → Redirected → 改向输入: %+v %+v %+v", es[0].Item, es[1].Item, es[2].Item)
	}
	if _, err := h.core.SubmitResult(n2.Tools.Calls[1].ID, txt("迟到"), ToolOK); err == nil {
		t.Fatalf("改向后迟到的结果应拒收")
	}
}

// ⑤a 改向后紧接取消：改向输入不丢，作为未消费插话交还（T17 先 flush 暂存项）。
func TestRedirect_ThenCancel(t *testing.T) {
	h := newHarness(t, 8)
	_, _ = h.core.Input(txt("问"), nil)
	_, _ = h.core.Steer(txt("改向"), nil, SteerRedirect)
	need, err := h.core.Cancel()
	if err != nil || need.Kind != NeedDone || need.Done.Reason.Kind != EndCancelled {
		t.Fatalf("应 Done(Cancelled): %v %+v", err, need)
	}
	if len(need.Done.Unconsumed) != 1 || need.Done.Unconsumed[0].Content[0].Text != "改向" {
		t.Fatalf("改向输入应交还为未消费插话: %+v", need.Done.Unconsumed)
	}
	if h.core.hdr.Redirected {
		t.Fatalf("取消后不应残留改向标记")
	}
}

// ⑤b 改向标记随快照恢复：恢复后回复到达仍按改向处理。
func TestRedirect_SurvivesRestore(t *testing.T) {
	h := newHarness(t, 8)
	n1, _ := h.core.Input(txt("问"), nil)
	_, _ = h.core.Steer(txt("改向"), nil, SteerRedirect)

	snap := h.core.Snapshot()
	b, _ := json.Marshal(snap)
	var snap2 Snapshot
	if err := json.Unmarshal(b, &snap2); err != nil {
		t.Fatal(err)
	}
	c2, err := Restore(snap2)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !c2.hdr.Redirected || len(c2.hdr.PendingInputs) != 1 {
		t.Fatalf("改向标记与暂存插话应随头部恢复")
	}
	calls := []ToolCall{{Name: "echo", Args: json.RawMessage(`{}`)}}
	need, err := c2.ModelDone(n1.Model.RequestID, n1.ID, AssistantItem{ToolCalls: calls}, StopReason{})
	if err != nil || need.Kind != NeedModel {
		t.Fatalf("恢复后应按改向处理: %v %+v", err, need)
	}
}
