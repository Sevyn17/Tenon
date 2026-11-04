package session

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// T2a-1（09 9.4 ②）：运行中到达的用户消息立即写进历史，进入下一次模型请求，不另开回合。
func TestMidInputAppendsDuringTurn(t *testing.T) {
	c := New(&Config{Model: "m", Policy: DefaultPolicy()})
	if _, err := c.Input(Content{NewTextPart("开始")}, map[string]any{"source": "user"}); err != nil {
		t.Fatal(err)
	}
	if err := c.MidInput(Content{NewTextPart("补充")}, map[string]any{"source": "user"}); err != nil {
		t.Fatalf("回合进行中应能插入: %v", err)
	}
	req, err := BuildRequest(c.Context())
	if err != nil {
		t.Fatal(err)
	}
	if n := len(req.Messages); n != 2 {
		t.Fatalf("应有两段用户输入，得 %d 条消息", n)
	}
	last := req.Messages[len(req.Messages)-1]
	if last.Role != "user" || len(last.Content) != 1 || last.Content[0].Text != "补充" {
		t.Fatalf("插入的消息应在末尾：%+v", last)
	}
	// 同一回合：不应产生新的 TurnInfo。
	if got := c.hdr.Turn.Turn; got != 1 {
		t.Fatalf("插入不应开新回合，turn=%d", got)
	}
}

// R14：工具结果未到齐时不能插入条目（插在调用与结果之间，厂商会拒绝该请求）；
// 也不能把这段区间里的 user 条目当作压缩切点。
func TestMidInputRefusedWhileToolsOpen(t *testing.T) {
	h := newHarness(t, 8)
	c := h.core
	n1, _ := c.Input(txt("开始"), nil)
	calls := []ToolCall{{Name: "echo", Args: json.RawMessage(`{"a":1}`)}}
	n2, err := c.ModelDone(n1.Model.RequestID, n1.ID, AssistantItem{ToolCalls: calls}, StopReason{V: "ToolUse"})
	if err != nil || n2.Kind != NeedTools {
		t.Fatalf("ModelDone: %v %+v", err, n2)
	}
	if err := c.MidInput(txt("补充"), nil); !errors.Is(err, ErrToolsOpen) {
		t.Fatalf("WaitingTools 时 MidInput 应返回 ErrToolsOpen，得 %v", err)
	}
	if err := c.Inject(KindStatus, "<status-update>x</status-update>", nil); !errors.Is(err, ErrToolsOpen) {
		t.Fatalf("WaitingTools 时 Inject 应返回 ErrToolsOpen，得 %v", err)
	}
	if c.CanCompact(c.absLen()) {
		t.Fatal("调用未闭合时末尾不应是合法压缩切点")
	}
	if _, err := c.SubmitResult(n2.Tools.Calls[0].ID, txt("r"), ToolOK); err != nil {
		t.Fatal(err)
	}
	// 结果到齐后（步边界之后、WaitingModel）可以插入，请求保持合法顺序。
	if err := c.MidInput(txt("补充"), nil); err != nil {
		t.Fatalf("结果到齐后应能插入: %v", err)
	}
	req, err := BuildRequest(c.Context())
	if err != nil {
		t.Fatal(err)
	}
	var roles []string
	for _, m := range req.Messages {
		roles = append(roles, m.Role)
	}
	if got := strings.Join(roles, ","); got != "user,assistant,tool,user" {
		t.Fatalf("请求顺序 %s，期望 user,assistant,tool,user", got)
	}
}

// 历史里已有「调用 → user → 结果」（旧版本写出的非法序列）时，user 不能闭合调用。
func TestCanCompactUserDoesNotCloseCalls(t *testing.T) {
	h := newHarness(t, 8)
	c := h.core
	n1, _ := c.Input(txt("开始"), nil)
	calls := []ToolCall{{Name: "echo", Args: json.RawMessage(`{"a":1}`)}}
	_, _ = c.ModelDone(n1.Model.RequestID, n1.ID, AssistantItem{ToolCalls: calls}, StopReason{V: "ToolUse"})
	c.appendEntry(c.hdr.Turn.Turn, nil, Item{Kind: KindUser, User: &UserItem{Content: txt("插话")}})
	if c.CanCompact(3) {
		t.Fatal("user 条目不应闭合未回结果的调用")
	}
}

func TestMidInputRequiresRunningTurn(t *testing.T) {
	c := New(&Config{Model: "m", Policy: DefaultPolicy()})
	if err := c.MidInput(Content{NewTextPart("补")}, nil); !errors.Is(err, ErrNotWaiting) {
		t.Fatalf("空闲时应返回 ErrNotWaiting，得 %v", err)
	}
}
