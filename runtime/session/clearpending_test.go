package session

// M4 回归：回合结束时清空全部「回合内暂存」字段（2026-11-12）。
//
// 这 7 个字段的有效性依赖 Status（只在非 Idle 时有意义），是「一个结构体 + 一个判别字段」
// 的形态。不拆子结构体（Header 直接 json.Marshal 存 sessions.header，字段名就是 schema），
// 而是把清零收进 Header.clearPending()，并在这里锁住「不会漏清」。
//
// 之前零测试保护：endTurn 手写 7 行清零，将来加字段漏清不会被发现。

import (
	"encoding/json"
	"testing"
)

// endTurn 后所有「回合内暂存」字段归零。
func TestEndTurnClearsAllPending(t *testing.T) {
	h := newHarness(t, 8)
	n1, err := h.core.Input(txt("问"), nil)
	if err != nil {
		t.Fatal(err)
	}
	// 把 7 个字段全部塞上值（模拟回合进行中的各种暂存）
	h.core.hdr.PendingCalls = []ToolCall{{Name: "echo", Args: json.RawMessage(`{}`)}}
	h.core.hdr.PendingInputs = []PendingInput{{Content: txt("插话"), Mode: SteerAppend}}
	h.core.hdr.PendingConfig = []ConfigPatch{{}}
	h.core.hdr.PendingCompact = &PendingCompact{Summary: txt("摘要"), Upto: 1}
	h.core.hdr.Redirected = true
	h.core.hdr.PendingModelRequestID = "req-x"
	if h.core.hdr.Turn == nil {
		t.Fatal("回合进行中应有 Turn")
	}

	// 结束回合
	if _, err := h.core.Abort(DoneReason()); err != nil {
		t.Fatal(err)
	}

	hdr := h.core.hdr
	if hdr.Status != StatusIdle {
		t.Errorf("Status 应为 Idle，得到 %v", hdr.Status)
	}
	if hdr.Turn != nil {
		t.Errorf("Turn 应清空，得到 %+v", hdr.Turn)
	}
	if hdr.PendingCalls != nil {
		t.Errorf("PendingCalls 应清空，得到 %+v", hdr.PendingCalls)
	}
	if hdr.PendingInputs != nil {
		t.Errorf("PendingInputs 应清空，得到 %+v", hdr.PendingInputs)
	}
	if hdr.PendingConfig != nil {
		t.Errorf("PendingConfig 应清空，得到 %+v", hdr.PendingConfig)
	}
	if hdr.PendingCompact != nil {
		t.Errorf("PendingCompact 应清空，得到 %+v", hdr.PendingCompact)
	}
	if hdr.Redirected {
		t.Error("Redirected 应清空")
	}
	if hdr.PendingModelRequestID != "" {
		t.Errorf("PendingModelRequestID 应清空，得到 %q", hdr.PendingModelRequestID)
	}
	_ = n1
}

// clearPending 本身：只清「回合内暂存」，不动回合外的字段（LogLen / CompactionBase / NextNeedID）。
func TestClearPendingKeepsDurableFields(t *testing.T) {
	h := newHarness(t, 8)
	if _, err := h.core.Input(txt("问"), nil); err != nil {
		t.Fatal(err)
	}
	hdr := h.core.hdr
	logLen, base, needID := hdr.LogLen, hdr.CompactionBase, hdr.NextNeedID
	hdr.PendingCalls = []ToolCall{{Name: "echo"}}
	hdr.Redirected = true

	hdr.clearPending()

	if hdr.LogLen != logLen {
		t.Errorf("LogLen 不该被清：%d → %d", logLen, hdr.LogLen)
	}
	if hdr.CompactionBase != base {
		t.Errorf("CompactionBase 不该被清：%d → %d", base, hdr.CompactionBase)
	}
	if hdr.NextNeedID != needID {
		t.Errorf("NextNeedID 不该被清：%d → %d", needID, hdr.NextNeedID)
	}
	if hdr.PendingCalls != nil || hdr.Redirected {
		t.Error("暂存字段应清空")
	}
}

// 清空后的 Header 能通过恢复校验（Idle 状态无残留），再序列化往返也一致。
func TestClearedHeaderRestoresAsIdle(t *testing.T) {
	h := newHarness(t, 8)
	if _, err := h.core.Input(txt("问"), nil); err != nil {
		t.Fatal(err)
	}
	h.core.hdr.PendingModelRequestID = "req-y"
	if _, err := h.core.Abort(DoneReason()); err != nil {
		t.Fatal(err)
	}
	// 序列化 → 反序列化 → 恢复校验（Idle 不应残留 request id）
	b, err := MarshalHeader(&h.core.hdr)
	if err != nil {
		t.Fatal(err)
	}
	hdr, err := UnmarshalHeader(b)
	if err != nil {
		t.Fatal(err)
	}
	if hdr.Status != StatusIdle {
		t.Fatalf("往返后应仍为 Idle，得到 %v", hdr.Status)
	}
	if hdr.PendingModelRequestID != "" {
		t.Fatalf("往返后不该残留请求 id，得到 %q", hdr.PendingModelRequestID)
	}
}
