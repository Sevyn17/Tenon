package session

// A1 完成判据：状态机全转移（T1–T20）+ checkpoint 落库 / 恢复往返 + ext 停顿落库时机
// + 确定性验收（注入时钟 / id）。

import (
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"
)

func testConfig(maxSteps uint32) *Config {
	return &Config{
		Model:  "main",
		System: []SystemBlock{{Text: "sys"}},
		Tools:  []ToolSpec{{Name: "echo", Description: "回显", Schema: json.RawMessage(`{"type":"object"}`)}},
		Policy: DefaultPolicy(),
	}
}

// newHarness：确定性内核（固定时钟步进 + 计数 id）。
func newHarness(t *testing.T, maxSteps uint32) *harness {
	n := new(int)
	clock := int64(0)
	h := &harness{t: t, core: New(testConfig(maxSteps),
		WithClock(func() time.Time { clock += 1000; return time.UnixMilli(clock) }),
		WithIDs(func() string { *n++; return "id-" + strconv.Itoa(*n) }))}
	h.core.hdr.Config.Policy.MaxSteps = maxSteps
	return h
}

type harness struct {
	t    *testing.T
	core *Core
}

func txt(s string) Content { return Content{NewTextPart(s)} }

func strPtr(s string) *string { return &s }

// —— T1 / T9：input 开回合，回复无调用 → Done ——

func TestT1_T9_InputThenDone(t *testing.T) {
	h := newHarness(t, 8)
	need, err := h.core.Input(txt("你好"), map[string]any{MetaSource: "user", MetaTrust: "trusted"})
	if err != nil {
		t.Fatalf("Input: %v", err)
	}
	if h.core.hdr.LogLen != 1 || h.core.items[0].Item.Kind != KindUser {
		t.Fatalf("T1 应追加一条 User 条目: LogLen=%d", h.core.hdr.LogLen)
	}
	if h.core.Status() != StatusWaitingModel || need.Kind != NeedModel || need.Model.RequestID == "" {
		t.Fatalf("T1 应 WaitingModel + Need::Model: %+v", need)
	}
	if h.core.hdr.Turn.Turn != 1 || h.core.hdr.Turn.Step != 1 {
		t.Fatalf("回合信息不符: %+v", h.core.hdr.Turn)
	}

	need, err = h.core.ModelDone(need.Model.RequestID, need.ID, AssistantItem{Content: txt("你好！")}, StopReason{V: "EndTurn"})
	if err != nil {
		t.Fatalf("ModelDone: %v", err)
	}
	if h.core.hdr.LogLen != 2 || need.Kind != NeedDone || need.Done.Reason.Kind != EndDone {
		t.Fatalf("T9 应写条目并 Done: LogLen=%d %+v", h.core.hdr.LogLen, need)
	}
	if h.core.Status() != StatusIdle || h.core.hdr.Turn != nil {
		t.Fatalf("T9 后应回 Idle")
	}
}

// —— T2：record 零步回合 ——

func TestT2_Record(t *testing.T) {
	h := newHarness(t, 8)
	if err := h.core.Record(txt("备注"), nil); err != nil || h.core.hdr.LogLen != 1 {
		t.Fatalf("Record: %v LogLen=%d", err, h.core.hdr.LogLen)
	}
	if h.core.Status() != StatusIdle || h.core.hdr.Turn != nil {
		t.Fatalf("T2 不应开启回合")
	}
	if h.core.items[0].Turn != 1 {
		t.Fatalf("record 应占新回合序号: %d", h.core.items[0].Turn)
	}
}

// —— T3 / T4：压缩与撤销（含压缩点回退）——

func TestT3_T4_CompactAndRewind(t *testing.T) {
	h := newHarness(t, 8)
	if err := h.core.Record(txt("a"), nil); err != nil {
		t.Fatalf("record a: %v", err)
	}
	if err := h.core.Record(txt("b"), nil); err != nil {
		t.Fatalf("record b: %v", err)
	}

	if err := h.core.Compact(txt("摘要"), 2, nil); err != nil || h.core.hdr.LogLen != 3 { // T3
		t.Fatalf("T3 Compact: %v LogLen=%d", err, h.core.hdr.LogLen)
	}
	if h.core.hdr.CompactionBase != 2 || h.core.items[2].Item.Kind != KindCompaction {
		t.Fatalf("压缩边界/条目不符: %d %+v", h.core.hdr.CompactionBase, h.core.items[2].Item)
	}

	if err := h.core.Rewind(1); err != nil { // T4：截掉压缩条目 → 退回上一次压缩 = 0
		t.Fatalf("T4 Rewind: %v", err)
	}
	if h.core.hdr.CompactionBase != 0 || h.core.hdr.LogLen != 1 {
		t.Fatalf("压缩点应退回 0，LogLen=1: %d/%d", h.core.hdr.CompactionBase, h.core.hdr.LogLen)
	}
	if err := h.core.Rewind(99); !errors.Is(err, ErrBadRewind) {
		t.Fatalf("越界 rewind 应 ErrBadRewind: %v", err)
	}
}

// T4：to 是回合首条 User 的下标。两回合普通对话 u1 a1 u2 a2：Rewind(2) 去掉第二回合，
// Rewind(0) 清空；Rewind(1)（assistant）、Rewind(3) 不是回合起点（2026-11-04 审查 P0-4：
// 原先 to 被当成"保留到 User 之后"，appapi 按 User 下标传入时普通会话必失败）。
func TestRewindToTurnStartIndex(t *testing.T) {
	h := newHarness(t, 8)
	for _, q := range []string{"u1", "u2"} {
		n, err := h.core.Input(txt(q), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.core.ModelDone(n.Model.RequestID, n.ID, AssistantItem{Content: txt("re:" + q)}, StopReason{}); err != nil {
			t.Fatal(err)
		}
	}
	if h.core.hdr.LogLen != 4 {
		t.Fatalf("两回合应有 4 条: %d", h.core.hdr.LogLen)
	}
	for _, bad := range []int{1, 3} {
		if err := h.core.Rewind(bad); !errors.Is(err, ErrBadRewind) {
			t.Fatalf("Rewind(%d) 不是回合起点，应 ErrBadRewind: %v", bad, err)
		}
	}
	if err := h.core.Rewind(2); err != nil {
		t.Fatalf("Rewind(2) 应退掉第二回合: %v", err)
	}
	if h.core.hdr.LogLen != 2 || h.core.items[1].Item.Kind != KindAssistant {
		t.Fatalf("应只剩第一回合 u1 a1: len=%d", h.core.hdr.LogLen)
	}
	if err := h.core.Rewind(2); err != nil { // to == 日志长度：不截，合法
		t.Fatalf("Rewind(日志长度) 应合法: %v", err)
	}
	if err := h.core.Rewind(0); err != nil || h.core.hdr.LogLen != 0 {
		t.Fatalf("Rewind(0) 应清空: %v len=%d", err, h.core.hdr.LogLen)
	}
}

// T5：等待态的 SetConfig 暂存；回合没到步边界就结束时也要生效，不能丢（审查 P0-12）。
func TestPendingConfigAppliedWhenTurnEnds(t *testing.T) {
	h := newHarness(t, 8)
	n, err := h.core.Input(txt("问"), nil)
	if err != nil {
		t.Fatal(err)
	}
	steps := uint32(3)
	if _, err := h.core.SetConfig(ConfigPatch{Model: strPtr("m2"), MaxSteps: &steps}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.core.ModelDone(n.Model.RequestID, n.ID, AssistantItem{Content: txt("答")}, StopReason{}); err != nil {
		t.Fatal(err)
	}
	if h.core.Status() != StatusIdle {
		t.Fatalf("回合应结束: %v", h.core.Status())
	}
	if c := h.core.hdr.Config; c.Model != "m2" || c.Policy.MaxSteps != 3 || len(h.core.hdr.PendingConfig) != 0 {
		t.Fatalf("暂存配置应在回合结束时生效: model=%s max=%d pending=%d", c.Model, c.Policy.MaxSteps, len(h.core.hdr.PendingConfig))
	}
}

// T4：截掉的压缩条目即使 upto 在截断点之前（压缩条目写在更后面），也要退回上一次压缩；
// 否则压缩基点仍指向已删的摘要，重建请求会丢掉摘要和它之前的全部历史。
func TestRewindPastCompactionEntryResetsBase(t *testing.T) {
	h := newHarness(t, 8)
	for _, s := range []string{"t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8"} {
		if err := h.core.Record(txt(s), nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.core.Compact(txt("摘要"), 4, nil); err != nil { // 压缩条目在 idx 8，upto=4
		t.Fatal(err)
	}
	if err := h.core.Record(txt("t9"), nil); err != nil {
		t.Fatal(err)
	}
	if err := h.core.Rewind(7); err != nil { // 截掉 idx 7.. 含压缩条目（idx 8）
		t.Fatalf("Rewind: %v", err)
	}
	if got := h.core.hdr.CompactionBase; got != 0 {
		t.Fatalf("压缩条目被截掉后基点应退回 0，得 %d", got)
	}
	req, err := BuildRequest(h.core.Context())
	if err != nil {
		t.Fatal(err)
	}
	if n := len(req.Messages); n != 7 {
		t.Fatalf("应保留 t1..t7 共 7 条，得 %d", n)
	}
	if _, err := Restore(h.core.Snapshot()); err != nil {
		t.Fatalf("截断后应能恢复: %v", err)
	}
}

// —— T5 / T6 / T10 / T11：等待态插话与改向 ——

func TestT5_T6_T10_T11_Steer(t *testing.T) {
	h := newHarness(t, 8)
	n1, _ := h.core.Input(txt("问"), nil)

	if _, err := h.core.Steer(txt("补充"), nil, SteerAppend); err != nil { // T5
		t.Fatalf("T5 Steer: %v", err)
	}
	if len(h.core.hdr.PendingInputs) != 1 {
		t.Fatalf("插话应暂存")
	}
	if _, err := h.core.SetConfig(ConfigPatch{Model: strPtr("cheap")}); err != nil { // T5
		t.Fatalf("T5 SetConfig: %v", err)
	}
	if h.core.hdr.Config.Model != "main" {
		t.Fatalf("等待态 set_config 不应立即生效")
	}

	need, err := h.core.ModelDone(n1.Model.RequestID, n1.ID, AssistantItem{Content: txt("答")}, StopReason{}) // T10
	if err != nil {
		t.Fatalf("T10 ModelDone: %v", err)
	}
	if need.Kind != NeedModel {
		t.Fatalf("T10 应进入下一步: %+v", need)
	}
	if h.core.hdr.Config.Model != "cheap" {
		t.Fatalf("步边界应应用暂存配置")
	}
	if h.core.items[len(h.core.items)-1].Item.Kind != KindUser {
		t.Fatalf("插话应在边界追加")
	}
	if h.core.hdr.Turn.Step != 2 {
		t.Fatalf("步数应推进到 2")
	}

	rid := need.Model.RequestID
	if _, err := h.core.Steer(txt("改向"), nil, SteerRedirect); err != nil { // T6
		t.Fatalf("T6 Steer: %v", err)
	}
	if !h.core.hdr.Redirected {
		t.Fatalf("T6 应标记改向")
	}
	calls := []ToolCall{{Name: "echo", Args: json.RawMessage(`{}`)}}
	need2, err := h.core.ModelDone(rid, need.ID, AssistantItem{Content: txt("答案"), ToolCalls: calls}, StopReason{}) // T11
	if err != nil {
		t.Fatalf("T11 ModelDone: %v", err)
	}
	if need2.Kind != NeedModel {
		t.Fatalf("T11 应发出新 Need::Model: %+v", need2)
	}
	last := h.core.items[len(h.core.items)-2] // 倒数第二：Cancelled(Redirected)；最后一条是边界追加的改向插话
	if last.Item.Kind != KindToolResult || last.Item.ToolResult.Status != ToolRedirected {
		t.Fatalf("T11 调用应补 Cancelled(Redirected): %+v", last.Item)
	}
}

// —— T12 / T15 / T16 / T20 / T17：工具循环全链 ——

func TestT12_T15_T16_T17_T20_ToolLoop(t *testing.T) {
	h := newHarness(t, 8)
	n1, _ := h.core.Input(txt("查天气"), nil)

	calls := []ToolCall{{Name: "echo", Args: json.RawMessage(`{"a":1}`)}, {Name: "echo", Args: json.RawMessage(`{"b":2}`)}}
	n2, err := h.core.ModelDone(n1.Model.RequestID, n1.ID, AssistantItem{ToolCalls: calls}, StopReason{V: "ToolUse"}) // T12
	if err != nil {
		t.Fatalf("T12: %v", err)
	}
	if n2.Kind != NeedTools || len(n2.Tools.Calls) != 2 {
		t.Fatalf("T12 应 Need::Tools: %+v", n2)
	}
	if n2.Tools.Calls[0].ID == "" || n2.Tools.Calls[0].ID == n2.Tools.Calls[1].ID {
		t.Fatalf("S6：内核 id 缺失或不唯一")
	}
	if h.core.Status() != StatusWaitingTools {
		t.Fatalf("T12 后应 WaitingTools")
	}

	n3, err := h.core.SubmitResult(calls[0].ID, txt("结果1"), ToolOK) // T15
	if err != nil {
		t.Fatalf("T15: %v", err)
	}
	if n3.Kind != NeedTools || len(n3.Tools.Calls) != 1 || n3.Tools.Calls[0].ID != calls[1].ID {
		t.Fatalf("T15 剩余调用不符: %+v", n3)
	}
	if _, err := h.core.SubmitResult("nope", nil, ToolOK); err == nil {
		t.Fatalf("S2：未知调用应拒收")
	}

	n4, err := h.core.SubmitResult(calls[1].ID, txt("结果2"), ToolOK) // T16 / T20
	if err != nil {
		t.Fatalf("T16: %v", err)
	}
	if n4.Kind != NeedModel || h.core.hdr.Turn.Step != 2 {
		t.Fatalf("T16 应进入下一步: %+v", n4)
	}

	n5, err := h.core.Cancel() // T17
	if err != nil {
		t.Fatalf("T17: %v", err)
	}
	if n5.Kind != NeedDone || n5.Done.Reason.Kind != EndCancelled {
		t.Fatalf("T17 应 Done(Cancelled): %+v", n5)
	}
	if h.core.Status() != StatusIdle {
		t.Fatalf("取消后应 Idle")
	}
}

// —— T7：WaitingTools 改向 ——

func TestT7_RedirectWhileWaitingTools(t *testing.T) {
	h := newHarness(t, 8)
	n1, _ := h.core.Input(txt("问"), nil)
	calls := []ToolCall{{Name: "echo", Args: json.RawMessage(`{}`)}, {Name: "echo", Args: json.RawMessage(`{}`)}}
	n2, _ := h.core.ModelDone(n1.Model.RequestID, n1.ID, AssistantItem{ToolCalls: calls}, StopReason{})
	_, _ = h.core.SubmitResult(n2.Tools.Calls[0].ID, txt("r1"), ToolOK)

	n3, err := h.core.Steer(txt("别查了"), nil, SteerRedirect)
	if err != nil {
		t.Fatalf("T7: %v", err)
	}
	if n3.Kind != NeedModel {
		t.Fatalf("T7 应立即进入步边界发 Need::Model: %+v", n3)
	}
	last := h.core.items[len(h.core.items)-1]
	if last.Item.Kind != KindUser { // 插话在边界追加
		t.Fatalf("T7 插话应追加: %+v", last.Item)
	}
	prev := h.core.items[len(h.core.items)-2]
	if prev.Item.Kind != KindToolResult || prev.Item.ToolResult.Status != ToolRedirected {
		t.Fatalf("T7 未回传调用应补 Cancelled(Redirected): %+v", prev.Item)
	}
}

// —— T8：空回复 ——

func TestT8_EmptyReply(t *testing.T) {
	h := newHarness(t, 8)
	n1, _ := h.core.Input(txt("问"), nil)
	_, _ = h.core.Steer(txt("补充"), nil, SteerAppend)
	nLog := len(h.core.items)
	n2, err := h.core.ModelDone(n1.Model.RequestID, n1.ID, AssistantItem{}, StopReason{})
	if err != nil {
		t.Fatalf("T8: %v", err)
	}
	if n2.Kind != NeedDone || n2.Done.Reason.Code != "empty response" {
		t.Fatalf("T8 应 Failed(empty response): %+v", n2)
	}
	if len(n2.Done.Unconsumed) != 1 {
		t.Fatalf("T8 待处理插话应交还: %+v", n2.Done)
	}
	if len(h.core.items) != nLog {
		t.Fatalf("T8 不应写条目")
	}
}

// —— T13：全部调用无效 ——

func TestT13_AllInvalidCalls(t *testing.T) {
	h := newHarness(t, 8)
	n1, _ := h.core.Input(txt("问"), nil)
	bad := []ToolCall{{Name: "ghost", Args: json.RawMessage(`{}`)}, {Name: "echo", Args: json.RawMessage(`broken`)}}
	n2, err := h.core.ModelDone(n1.Model.RequestID, n1.ID, AssistantItem{ToolCalls: bad}, StopReason{})
	if err != nil {
		t.Fatalf("T13: %v", err)
	}
	if n2.Kind != NeedModel {
		t.Fatalf("T13 应回 Need::Model: %+v", n2)
	}
	for i := 0; i < 2; i++ {
		e := h.core.items[len(h.core.items)-2+i]
		if e.Item.Kind != KindToolResult || e.Item.ToolResult.Status != ToolError {
			t.Fatalf("T13 应回填错误结果: %+v", e.Item)
		}
	}
}

// —— T14：失败与中断 ——

func TestT14_FailAndInterrupt(t *testing.T) {
	h := newHarness(t, 8)
	n1, _ := h.core.Input(txt("问"), nil)
	n2, err := h.core.ModelFailed(n1.Model.RequestID, n1.ID, FailedReason("timeout"))
	if err != nil || n2.Done.Reason.Kind != EndFailed {
		t.Fatalf("T14 失败: %v %+v", err, n2)
	}

	n3, err := h.core.Resume() // 失败重试（末尾是 User 条目 → 沿用回合）
	if err != nil || n3 == nil || n3.Kind != NeedModel {
		t.Fatalf("resume 应恢复: %v %+v", err, n3)
	}
	n4, err := h.core.ModelInterrupted(n3.Model.RequestID, n3.ID,
		AssistantItem{Content: txt("半截"), ToolCalls: []ToolCall{{Name: "echo", Args: json.RawMessage(`{}`)}}},
		FailedReason("stream_reset"))
	if err != nil {
		t.Fatalf("T14 中断: %v", err)
	}
	if n4.Done.Reason.Code != "stream_reset" {
		t.Fatalf("中断原因不符: %+v", n4)
	}
	a := h.core.items[len(h.core.items)-2]
	if a.Item.Kind != KindAssistant || !a.Item.Assistant.Interrupted {
		t.Fatalf("中断条目应带 interrupted: true: %+v", a.Item)
	}
	r := h.core.items[len(h.core.items)-1]
	if r.Item.Kind != KindToolResult || r.Item.ToolResult.Status != ToolTurnCancelled {
		t.Fatalf("中断调用应补 Cancelled(TurnCancelled): %+v", r.Item)
	}
}

// —— T18 / T19：abort 与步数上限 ——

func TestT18_T19(t *testing.T) {
	h := newHarness(t, 2)
	n1, _ := h.core.Input(txt("问"), nil)
	_ = n1 // 初值未读：346 行复用该变量（SA4006 占位）
	_, err := h.core.Abort(FailedReason("recover_loop"))
	if err != nil {
		t.Fatalf("T18: %v", err)
	}
	if h.core.Status() != StatusIdle {
		t.Fatalf("T18 后应 Idle")
	}

	n1, _ = h.core.Input(txt("再问"), nil)
	call := []ToolCall{{Name: "echo", Args: json.RawMessage(`{}`)}}
	n2, _ := h.core.ModelDone(n1.Model.RequestID, n1.ID, AssistantItem{ToolCalls: call}, StopReason{})
	n3, _ := h.core.SubmitResult(n2.Tools.Calls[0].ID, txt("r"), ToolOK) // 步 2
	if n3.Kind != NeedModel {
		t.Fatalf("步 2 应发出: %+v", n3)
	}
	n4, _ := h.core.ModelDone(n3.Model.RequestID, n3.ID, AssistantItem{ToolCalls: call}, StopReason{})
	n5, _ := h.core.SubmitResult(n4.Tools.Calls[0].ID, txt("r"), ToolOK) // 边界：已达上限
	if n5.Kind != NeedDone || n5.Done.Reason.Kind != EndMaxSteps {
		t.Fatalf("T19 应 Done(MaxSteps): %+v", n5)
	}
}

// —— S1 / S4：原子性与上限 ——

func TestS1_S4_AtomicityAndLimits(t *testing.T) {
	h := newHarness(t, 8)
	big := map[string]any{"k": make([]byte, 1<<20)}
	if _, err := h.core.Input(txt("问"), big); !errors.Is(err, ErrExtTooLarge) {
		t.Fatalf("meta 超限应 ErrExtTooLarge: %v", err)
	}
	if h.core.Status() != StatusIdle || len(h.core.items) != 0 {
		t.Fatalf("S1：失败调用不得改变状态")
	}
	if err := h.core.SetExt("app.big", make([]byte, 1<<20)); !errors.Is(err, ErrExtTooLarge) {
		t.Fatalf("ext 超限应 ErrExtTooLarge: %v", err)
	}
	if _, err := h.core.Cancel(); !errors.Is(err, ErrNotWaiting) {
		t.Fatalf("Idle cancel 应 ErrNotWaiting: %v", err)
	}
	if _, err := h.core.Steer(txt("x"), nil, SteerAppend); !errors.Is(err, ErrNotWaiting) {
		t.Fatalf("Idle steer 应 ErrNotWaiting: %v", err)
	}
}

// —— S2 / S7：过期结果拒收 ——

func TestS2_S7_StaleResult(t *testing.T) {
	h := newHarness(t, 8)
	n1, _ := h.core.Input(txt("问"), nil)
	if _, err := h.core.ModelDone("wrong-request", n1.ID, AssistantItem{Content: txt("x")}, StopReason{}); !errors.Is(err, ErrState) {
		t.Fatalf("S7：request id 不符应拒收: %v", err)
	}
	if _, err := h.core.ModelDone(n1.Model.RequestID, n1.ID-1, AssistantItem{Content: txt("x")}, StopReason{}); !errors.Is(err, ErrState) {
		t.Fatalf("S2：need id 不符应拒收: %v", err)
	}
}

// —— 快照往返 + Restore 校验（S5 / R14 / R15）——

func TestSnapshotRoundTripAndRestore(t *testing.T) {
	h := newHarness(t, 8)
	n1, _ := h.core.Input(txt("问"), nil)
	n2, _ := h.core.ModelDone(n1.Model.RequestID, n1.ID,
		AssistantItem{Content: txt("答"), ToolCalls: []ToolCall{{Name: "echo", Args: json.RawMessage(`{}`)}}, Usage: Usage{Input: 10, Output: 5, Source: "Reported"}}, StopReason{})
	_, _ = h.core.SubmitResult(n2.Tools.Calls[0].ID, txt("r"), ToolOK)

	snap := h.core.Snapshot()
	hb, _ := json.Marshal(snap.Header)
	var h2 Header
	if err := json.Unmarshal(hb, &h2); err != nil {
		t.Fatalf("头部往返: %v", err)
	}
	var entries []*Entry
	for _, e := range snap.Entries {
		eb, _ := json.Marshal(e)
		var e2 Entry
		if err := json.Unmarshal(eb, &e2); err != nil {
			t.Fatalf("条目往返: %v", err)
		}
		entries = append(entries, &e2)
	}
	snap2 := Snapshot{Header: h2, Base: snap.Base, Entries: entries}
	c2, err := Restore(snap2)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if c2.Status() != StatusWaitingModel || c2.hdr.LogLen != snap.Header.LogLen {
		t.Fatalf("恢复后状态不符（应为步 2 WaitingModel）: %+v", c2.hdr)
	}

	// S5：未提交尾部（超出 LogLen）→ 截断丢弃。
	snap3 := snap2
	extra := &Entry{V: 1, ID: "extra", Turn: 9, Item: Item{Kind: KindUser, User: &UserItem{Content: txt("未提交")}}}
	snap3.Entries = append(append([]*Entry(nil), snap2.Entries...), extra)
	c3, err := Restore(snap3)
	if err != nil {
		t.Fatalf("S5 Restore: %v", err)
	}
	if c3.hdr.LogLen != snap.Header.LogLen || len(c3.items) != len(snap2.Entries) {
		t.Fatalf("S5 未提交条目应被丢弃")
	}

	// R14：结果不按调用顺序 → 拒绝。
	badEntries := make([]*Entry, 0, len(snap2.Entries))
	for _, e := range snap2.Entries {
		eb, _ := json.Marshal(e)
		var ec Entry
		_ = json.Unmarshal(eb, &ec)
		badEntries = append(badEntries, &ec)
	}
	for _, e := range badEntries {
		if e.Item.Kind == KindToolResult {
			e.Item.ToolResult.CallID = "ghost"
		}
	}
	if _, err := Restore(Snapshot{Header: h2, Base: snap.Base, Entries: badEntries}); err == nil {
		t.Fatalf("R14：乱序结果应拒绝恢复")
	}

	// R15：未知条目 → 只读。
	unk := &Entry{V: 1, ID: "unk", Turn: 1, Item: Item{Kind: KindUnknown, Unknown: &UnknownItem{Raw: json.RawMessage(`{"kind":"martian"}`)}}}
	hl := h2 // 未知条目自身是已提交历史：LogLen 同步 +1，否则会被 S5 当未提交丢弃
	hl.LogLen = snap.Header.LogLen + 1
	c4, err := Restore(Snapshot{Header: hl, Base: snap.Base, Entries: append(append([]*Entry(nil), snap2.Entries...), unk)})
	if err != nil {
		t.Fatalf("R15 Restore: %v", err)
	}
	if !c4.ReadOnly() {
		t.Fatalf("未知条目应使会话只读")
	}
	if _, err := c4.Input(txt("x"), nil); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("只读会话写命令应拒绝: %v", err)
	}
}

// —— S6：恢复后调用 id 沿用（T27 挂起恢复）——

func TestS6_CallIDStableAcrossRestore(t *testing.T) {
	h := newHarness(t, 8)
	n1, _ := h.core.Input(txt("问"), nil)
	calls := []ToolCall{{Name: "echo", Args: json.RawMessage(`{}`)}, {Name: "echo", Args: json.RawMessage(`{}`)}}
	n2, _ := h.core.ModelDone(n1.Model.RequestID, n1.ID, AssistantItem{ToolCalls: calls}, StopReason{})
	id1 := n2.Tools.Calls[0].ID

	snap := h.core.Snapshot()
	c2, err := Restore(snap)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if c2.Status() != StatusWaitingTools || len(c2.hdr.PendingCalls) != 2 {
		t.Fatalf("恢复后应回到 WaitingTools")
	}
	if c2.hdr.PendingCalls[0].ID != id1 {
		t.Fatalf("S6：恢复后调用 id 应沿用: %s != %s", c2.hdr.PendingCalls[0].ID, id1)
	}
}

// —— 请求构造：BuildRequest / BuildWithCompaction / BuildBranch ——

func TestBuildRequests(t *testing.T) {
	h := newHarness(t, 8)
	if err := h.core.Record(txt("a"), nil); err != nil {
		t.Fatalf("record a: %v", err)
	}
	if err := h.core.Record(txt("b"), nil); err != nil {
		t.Fatalf("record b: %v", err)
	}
	if err := h.core.Compact(txt("摘要"), 2, nil); err != nil {
		t.Fatalf("compact: %v", err)
	}
	if err := h.core.Record(txt("c"), nil); err != nil {
		t.Fatalf("record c: %v", err)
	}

	ctx := h.core.Context()
	req, err := BuildRequest(ctx)
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if len(req.Messages) != 2 { // 压缩条目取代 [0,2)，条目 a 不发
		t.Fatalf("消息数不符: %d %+v", len(req.Messages), req.Messages)
	}
	if req.Messages[0].Content[0].Text != "摘要" || req.Messages[1].Content[0].Text != "c" {
		t.Fatalf("消息展开不符: %+v", req.Messages)
	}

	req2, err := BuildWithCompaction(ctx, txt("新摘要"), 3)
	if err != nil {
		t.Fatalf("BuildWithCompaction: %v", err)
	}
	if len(req2.Messages) != 2 || req2.Messages[0].Content[0].Text != "新摘要" || req2.Messages[1].Content[0].Text != "c" {
		t.Fatalf("压缩请求不符: %+v", req2)
	}

	req3, err := BuildBranch(ctx, txt("有什么要记住的吗？"))
	if err != nil || len(req3.Messages) != 3 {
		t.Fatalf("BuildBranch: %v %d", err, len(req3.Messages))
	}
}

// 失败 / 中断结束回合时，暂存插话原样交还（T17；审查 P1-19：原先只有取消路径交还）。
func TestFailedAndInterruptedHandBackPendingInputs(t *testing.T) {
	for _, fail := range []bool{true, false} {
		h := newHarness(t, 8)
		n, err := h.core.Input(txt("问"), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.core.Steer(txt("插话"), nil, SteerAppend); err != nil { // 暂存到步边界（MidInput 直接进历史，不算暂存）
			t.Fatal(err)
		}
		var need *Need
		if fail {
			need, err = h.core.ModelFailed(n.Model.RequestID, n.ID, FailedReason("x"))
		} else {
			need, err = h.core.ModelInterrupted(n.Model.RequestID, n.ID, AssistantItem{}, CancelledReason())
		}
		if err != nil {
			t.Fatal(err)
		}
		if need.Kind != NeedDone || len(need.Done.Unconsumed) != 1 {
			t.Fatalf("fail=%v：插话应交还，得 %+v", fail, need.Done)
		}
	}
}

// Config 为 nil 的内核收到工具调用不 panic（审查 P1-20，R7）。
func TestNilConfigToolCallNoPanic(t *testing.T) {
	c := New(nil)
	n, err := c.Input(txt("问"), nil)
	if err != nil {
		t.Fatal(err)
	}
	calls := []ToolCall{{Name: "echo", Args: []byte(`{}`)}}
	if _, err := c.ModelDone(n.Model.RequestID, n.ID, AssistantItem{ToolCalls: calls}, StopReason{}); err != nil {
		t.Fatalf("ModelDone: %v", err)
	}
}

// Steer 不改调用方传入的 meta（审查 P1-21）。
func TestSteerDoesNotMutateCallerMeta(t *testing.T) {
	h := newHarness(t, 8)
	if _, err := h.core.Input(txt("问"), nil); err != nil {
		t.Fatal(err)
	}
	m := map[string]any{MetaSource: "user"}
	if _, err := h.core.Steer(txt("插话"), m, SteerAppend); err != nil {
		t.Fatal(err)
	}
	if len(m) != 1 {
		t.Fatalf("调用方 meta 被改：%v", m)
	}
}
