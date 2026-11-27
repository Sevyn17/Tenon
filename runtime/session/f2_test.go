package session

// Mortise docs/63 F2：名字不在冻结清单里的调用置 Unknown，不走 T13 立即回填，
// 由执行侧（toolhub）按当前登记再判一次（已放宽 / 真不存在），顺序照旧由执行侧保证。

import (
	"encoding/json"
	"testing"
)

// 一步只调一个名字未知的工具：照常发 Need::Tools，Unknown 置位，Hints 仍给"不存在"文案。
func TestF2_UnknownCallGoesToExecutor(t *testing.T) {
	h := newHarness(t, 8)
	n1, _ := h.core.Input(txt("问"), nil)
	calls := []ToolCall{{Name: "ghost", Args: json.RawMessage(`{}`)}}
	n2, err := h.core.ModelDone(n1.Model.RequestID, n1.ID, AssistantItem{ToolCalls: calls}, StopReason{})
	if err != nil {
		t.Fatalf("ModelDone: %v", err)
	}
	if n2.Kind != NeedTools {
		t.Fatalf("有 Unknown 调用时应发 Need::Tools: %+v", n2)
	}
	id := n2.Tools.Calls[0].ID
	if !n2.Tools.Unknown[id] {
		t.Fatalf("Unknown 应置位: %+v", n2.Tools)
	}
	if n2.Tools.Hints[id] == "" {
		t.Fatalf("Hints 仍应给不存在文案: %+v", n2.Tools)
	}
}

// 已知工具的调用不置 Unknown；参数坏 / 超上限的不算 Unknown。
func TestF2_UnknownOnlyForUnknownNames(t *testing.T) {
	h := newHarness(t, 8)
	n1, _ := h.core.Input(txt("问"), nil)
	calls := []ToolCall{{Name: "echo", Args: json.RawMessage(`{}`)}, {Name: "echo", Args: json.RawMessage(`broken`)}, {Name: "ghost", Args: json.RawMessage(`{}`)}}
	n2, err := h.core.ModelDone(n1.Model.RequestID, n1.ID, AssistantItem{ToolCalls: calls}, StopReason{})
	if err != nil || n2.Kind != NeedTools {
		t.Fatalf("应发 Need::Tools: %v %+v", err, n2)
	}
	cs := n2.Tools.Calls
	if n2.Tools.Unknown[cs[0].ID] || n2.Tools.Unknown[cs[1].ID] || !n2.Tools.Unknown[cs[2].ID] {
		t.Fatalf("Unknown 只应标名字未知的调用: %+v", n2.Tools.Unknown)
	}
}

// 恢复路径（restore.go 重算 hints）同样带 Unknown。
func TestF2_UnknownSurvivesRestore(t *testing.T) {
	h := newHarness(t, 8)
	n1, _ := h.core.Input(txt("问"), nil)
	calls := []ToolCall{{Name: "ghost", Args: json.RawMessage(`{}`)}}
	if _, err := h.core.ModelDone(n1.Model.RequestID, n1.ID, AssistantItem{ToolCalls: calls}, StopReason{}); err != nil {
		t.Fatal(err)
	}
	c, err := Restore(h.core.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	need := c.Resumed()
	if need == nil || need.Kind != NeedTools || !need.Tools.Unknown[need.Tools.Calls[0].ID] {
		t.Fatalf("恢复后应带 Unknown: %+v", need)
	}
}
