package session

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// 截断的工具参数（max_tokens 截在调用中途、流被中断）不能原样进条目：
// 条目要能序列化（检查点）、请求要能哈希，模型拿到「参数不是合法 JSON」的提示。
func TestInvalidToolArgsNormalized(t *testing.T) {
	h := newHarness(t, 8)
	c := h.core
	n1, _ := c.Input(txt("开始"), nil)
	calls := []ToolCall{{Name: "echo", Args: json.RawMessage(`{"a":`)}}
	n2, err := c.ModelDone(n1.Model.RequestID, n1.ID, AssistantItem{ToolCalls: calls}, StopReason{V: "ToolUse"})
	if err != nil {
		t.Fatalf("ModelDone: %v", err)
	}
	if n2.Kind != NeedModel {
		t.Fatalf("唯一调用参数非法 → 全部无效 → 回填错误后进入下一步，得 %+v", n2)
	}
	for i, e := range c.items {
		if _, err := json.Marshal(e); err != nil {
			t.Fatalf("条目 %d 无法序列化: %v", i, err)
		}
	}
	req, err := BuildRequest(c.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := json.Marshal(req); err != nil {
		t.Fatalf("请求无法序列化: %v", err)
	}
	var tool string
	for _, m := range req.Messages {
		if m.Role == "tool" {
			for _, p := range m.Content {
				tool += p.Text
			}
		}
	}
	if !strings.Contains(tool, "不是合法 JSON") {
		t.Fatalf("模型应收到参数非法的提示：%q", tool)
	}
}

// 流中断时同样规整（中断恰恰是半截参数最常见的来源）。
func TestInvalidToolArgsNormalizedOnInterrupt(t *testing.T) {
	h := newHarness(t, 8)
	c := h.core
	n1, _ := c.Input(txt("开始"), nil)
	calls := []ToolCall{{Name: "echo", Args: json.RawMessage(`{"path":"a`)}}
	if _, err := c.ModelInterrupted(n1.Model.RequestID, n1.ID, AssistantItem{ToolCalls: calls}, CancelledReason()); err != nil {
		t.Fatalf("ModelInterrupted: %v", err)
	}
	for i, e := range c.items {
		if _, err := json.Marshal(e); err != nil {
			t.Fatalf("条目 %d 无法序列化: %v", i, err)
		}
	}
	if _, err := Restore(c.Snapshot()); err != nil {
		t.Fatalf("应能恢复: %v", err)
	}
}

// D 组：厂商调用 id 重复（同一回复里、或与历史里的重复）时改用内核 id——一个请求里 tool_use id 必须唯一。
func TestDuplicateWireIDsReplaced(t *testing.T) {
	h := newHarness(t, 8)
	c := h.core
	n1, _ := c.Input(txt("开始"), nil)
	calls := []ToolCall{{Name: "echo", WireID: "call_0", Args: json.RawMessage(`{}`)}, {Name: "echo", WireID: "call_0", Args: json.RawMessage(`{}`)}}
	n2, err := c.ModelDone(n1.Model.RequestID, n1.ID, AssistantItem{ToolCalls: calls}, StopReason{V: "ToolUse"})
	if err != nil {
		t.Fatal(err)
	}
	var n3 *Need
	for _, tc := range n2.Tools.Calls {
		n3, _ = c.SubmitResult(tc.ID, txt("r"), ToolOK)
	}
	// 下一步厂商又从 call_0 起编：也要换掉。
	_, err = c.ModelDone(n3.Model.RequestID, n3.ID, AssistantItem{ToolCalls: []ToolCall{{Name: "echo", WireID: "call_0", Args: json.RawMessage(`{}`)}}}, StopReason{V: "ToolUse"})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range c.items {
		if e.Item.Kind != KindAssistant {
			continue
		}
		for _, tc := range e.Item.Assistant.ToolCalls {
			if seen[tc.WireID] {
				t.Fatalf("wire id 重复：%s", tc.WireID)
			}
			seen[tc.WireID] = true
		}
	}
	// Restore 校验同一条目内不重复，这里应能恢复。
	if _, err := Restore(c.Snapshot()); err != nil {
		t.Fatalf("应能恢复: %v", err)
	}
}

// D 组：SetExt 超限时恢复原值（要么完整生效，要么不变）。
func TestSetExtTooLargeKeepsOld(t *testing.T) {
	h := newHarness(t, 8)
	c := h.core
	if err := c.SetExt("app.k", "v1"); err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("x", 128<<10)
	if err := c.SetExt("app.k", big); !errors.Is(err, ErrExtTooLarge) {
		t.Fatalf("超限应报 ErrExtTooLarge，得 %v", err)
	}
	if got := c.hdr.Ext["app.k"]; got != "v1" {
		t.Fatalf("超限时应保留原值，得 %v", got)
	}
	if err := c.SetExt("app.new", big); !errors.Is(err, ErrExtTooLarge) {
		t.Fatal("超限应报错")
	}
	if _, ok := c.hdr.Ext["app.new"]; ok {
		t.Fatal("超限时新键不应留下")
	}
}
