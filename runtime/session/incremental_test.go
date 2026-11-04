package session

import (
	"encoding/json"
	"reflect"
	"testing"
)

// buildToolTurns 跑 n 个回合，每回合一次带调用的模型回复 + 结果 + 结尾文本，返回内核。
func buildToolTurns(t *testing.T, n int) *Core {
	t.Helper()
	h := newHarness(t, 8)
	for i := 0; i < n; i++ {
		_, nd, err := h.core.Input(txt("问"), nil)
		if err != nil {
			t.Fatal(err)
		}
		calls := []ToolCall{{Name: "echo", WireID: "call_0", Args: json.RawMessage(`{}`)}} // 每轮都从 call_0 起编
		_, nd, err = h.core.ModelDone(nd.Model.RequestID, nd.ID, AssistantItem{ToolCalls: calls}, StopReason{})
		if err != nil {
			t.Fatal(err)
		}
		_, nd, err = h.core.SubmitResult(nd.Tools.Calls[0].ID, txt("结果"), ToolOK)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err = h.core.ModelDone(nd.Model.RequestID, nd.ID, AssistantItem{Content: txt("好")}, StopReason{}); err != nil {
			t.Fatal(err)
		}
	}
	return h.core
}

// CompactPoints 与逐个 CanCompact 结果一致（审查 P1-40：压缩器找切点改用它，一次遍历）。
func TestCompactPointsMatchesCanCompact(t *testing.T) {
	c := buildToolTurns(t, 5)
	var want []int
	for u := 0; u <= c.absLen(); u++ {
		if c.CanCompact(u) {
			want = append(want, u)
		}
	}
	if got := c.CompactPoints(); !reflect.DeepEqual(got, want) {
		t.Fatalf("CompactPoints = %v，逐个判定 = %v", got, want)
	}
}

// 厂商调用 id 去重的缓存随追加更新、随 Rewind 失效：每轮都叫 call_0 的厂商，每条调用的 wire id 仍唯一；
// 退回之后被截掉的 id 可以重新用（审查 P1-40：原先每次 ModelDone 全量扫加载窗口）。
func TestWireIDCacheTracksAppendAndRewind(t *testing.T) {
	c := buildToolTurns(t, 3)
	seen := map[string]bool{}
	for _, e := range c.items {
		if a := e.Item.Assistant; a != nil {
			for _, tc := range a.ToolCalls {
				if seen[tc.WireID] {
					t.Fatalf("wire id %q 重复", tc.WireID)
				}
				seen[tc.WireID] = true
			}
		}
	}
	if _, err := c.Rewind(0); err != nil {
		t.Fatal(err)
	}
	if u := c.usedWireIDs(); len(u) != 0 {
		t.Fatalf("Rewind(0) 后不应还有已用 id：%v", u)
	}
	_, nd, _ := c.Input(txt("再问"), nil)
	_, _, err := c.ModelDone(nd.Model.RequestID, nd.ID, AssistantItem{ToolCalls: []ToolCall{{Name: "echo", WireID: "call_0", Args: json.RawMessage(`{}`)}}}, StopReason{})
	if err != nil {
		t.Fatal(err)
	}
	last := c.items[len(c.items)-1].Item.Assistant
	if last.ToolCalls[0].WireID != "call_0" {
		t.Fatalf("Rewind 后 call_0 应可重新使用，得 %q", last.ToolCalls[0].WireID)
	}
}

// 快照与内核共享条目切片、不复制：Rewind 之后再追加，不能改写之前发出去的快照
// （审查 P1-40：agent.publishSnapshot 因此不再每次提交复制整个切片）。
func TestSnapshotNotOverwrittenAfterRewind(t *testing.T) {
	c := buildToolTurns(t, 2)
	snap := c.Snapshot()
	before := append([]*Entry(nil), snap.Entries...)
	if _, err := c.Rewind(4); err != nil {
		t.Fatal(err)
	}
	_, nd, _ := c.Input(txt("新问题"), nil)
	if _, _, err := c.ModelDone(nd.Model.RequestID, nd.ID, AssistantItem{Content: txt("新答")}, StopReason{}); err != nil {
		t.Fatal(err)
	}
	for i := range before {
		if snap.Entries[i] != before[i] {
			t.Fatalf("Rewind 后追加改写了旧快照的第 %d 条", i)
		}
	}
}
