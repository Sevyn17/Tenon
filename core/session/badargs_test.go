package session

import (
	"encoding/json"
	"strings"
	"testing"
)

// 截断的工具参数（max_tokens 截在调用中途、流被中断）不能原样进条目：
// 条目要能序列化（检查点）、请求要能哈希，模型拿到「参数不是合法 JSON」的提示。
func TestInvalidToolArgsNormalized(t *testing.T) {
	h := newHarness(t, 8)
	c := h.core
	_, n1, _ := c.Input(txt("开始"), nil)
	calls := []ToolCall{{Name: "echo", Args: json.RawMessage(`{"a":`)}}
	_, n2, err := c.ModelDone(n1.Model.RequestID, n1.ID, AssistantItem{ToolCalls: calls}, StopReason{V: "ToolUse"})
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
	_, n1, _ := c.Input(txt("开始"), nil)
	calls := []ToolCall{{Name: "echo", Args: json.RawMessage(`{"path":"a`)}}
	if _, _, err := c.ModelInterrupted(n1.Model.RequestID, n1.ID, AssistantItem{ToolCalls: calls}, CancelledReason()); err != nil {
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
