package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"ybase-kernel/core/callctx"
	"ybase-kernel/core/inject"
	"ybase-kernel/core/reqlog"
	"ybase-kernel/core/session"
)

// recordModel：单次调用直接回文本的模型，保留每次请求便于断言（T2b 验收）。
type recordModel struct {
	mu    sync.Mutex
	reply string
	reqs  []session.Request
}

func (m *recordModel) Stream(_ context.Context, req session.Request, onText func(string)) (session.AssistantItem, session.StopReason, error) {
	m.mu.Lock()
	m.reqs = append(m.reqs, req)
	m.mu.Unlock()
	text := m.reply
	if text == "" {
		text = "好"
	}
	if onText != nil {
		onText(text)
	}
	return session.AssistantItem{Content: session.Content{session.NewTextPart(text)}}, session.StopReason{V: "EndTurn"}, nil
}

func (m *recordModel) last() session.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reqs[len(m.reqs)-1]
}

func (m *recordModel) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.reqs)
}

// requestText 拼出请求里全部消息文本，便于断言注入内容确实被模型看到。
func requestText(req session.Request) string {
	var b strings.Builder
	for _, msg := range req.Messages {
		for _, p := range msg.Content {
			b.WriteString(p.Text)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// injected 返回历史里指定种类的注入条目文本。
func injected(a *Agent, kind string) []string {
	var out []string
	for _, e := range a.Core.Context().Items {
		if e.Item.Kind == kind && e.Item.Injection != nil {
			out = append(out, e.Item.Injection.Text)
		}
	}
	return out
}

// TestInjectStatusOncePerChange：同一状态连续两次调模型只注入一次（09 9.5 验收）。
func TestInjectStatusOncePerChange(t *testing.T) {
	db := tempStore(t)
	a := newAgent(t, db, agentOpts{model: fakeModel{}})
	var calls int
	a.Statuses = []inject.Reg{{
		Key: "todo", Owner: "todo",
		Fn: func(*callctx.Ctx) []inject.Item {
			calls++
			return []inject.Item{{Body: "任务 1"}}
		},
	}}
	if _, err := a.Send(context.Background(), "ping"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if calls != 2 {
		t.Fatalf("伪模型应走工具回合、两次调模型，实际 %d", calls)
	}
	if got := injected(a, session.KindStatus); len(got) != 1 {
		t.Fatalf("首回合应只注入一次状态，实际 %d：%v", len(got), got)
	}
	// 第二轮状态没变：不再注入，历史里仍只有一条 status。
	if _, err := a.Send(context.Background(), "再来"); err != nil {
		t.Fatalf("Send 2: %v", err)
	}
	if calls != 4 {
		t.Fatalf("第二轮应再调两次模型，实际 %d", calls)
	}
	if got := injected(a, session.KindStatus); len(got) != 1 {
		t.Fatalf("未变化的状态不应重复注入，实际 %d：%v", len(got), got)
	}
}

// TestInjectStatusChanged：状态变化后第二次调模型注入新内容。
func TestInjectStatusChanged(t *testing.T) {
	db := tempStore(t)
	a := newAgent(t, db, agentOpts{model: &toolThenTextModel{}})
	var calls int
	a.Statuses = []inject.Reg{{
		Key: "todo", Owner: "todo",
		Fn: func(*callctx.Ctx) []inject.Item {
			calls++
			return []inject.Item{{Body: fmt.Sprintf("任务 %d", calls)}}
		},
	}}
	if _, err := a.Send(context.Background(), "ping"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	got := injected(a, session.KindStatus)
	if len(got) != 2 || !strings.Contains(got[0], "任务 1") || !strings.Contains(got[1], "任务 2") {
		t.Fatalf("变化的状态应各注入一次：%v", got)
	}
}

// TestInjectBudgetKeepsHighPriority：超总预算时低优先级条目被丢弃，并在条目 meta 里留记录。
func TestInjectBudgetKeepsHighPriority(t *testing.T) {
	db := tempStore(t)
	m := &recordModel{}
	a := newAgent(t, db, agentOpts{model: m})
	keys := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}
	a.Statuses = []inject.Reg{{
		Key: "many", Owner: "many",
		Fn: func(*callctx.Ctx) []inject.Item {
			var items []inject.Item
			for _, k := range keys {
				items = append(items, inject.Item{Key: k, Body: k + strings.Repeat("x", 1000), Budget: 200})
			}
			return items
		},
	}}
	if _, err := a.Send(context.Background(), "hi"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	var entry *session.Entry
	for _, e := range a.Core.Context().Items {
		if e.Item.Kind == session.KindStatus {
			entry = e
		}
	}
	if entry == nil {
		t.Fatal("没有 status 条目")
	}
	dropped, _ := entry.Meta[inject.MetaDropped].([]string)
	if len(dropped) == 0 {
		t.Fatalf("应记录被丢弃的 key：%+v", entry.Meta)
	}
	if dropped[0] != "j" {
		t.Fatalf("应丢弃最低优先级的 j，实际 %v", dropped)
	}
	hashes, _ := entry.Meta[inject.MetaStatus].(map[string]string)
	if _, ok := hashes["j"]; ok {
		t.Fatalf("被丢弃的 key 不应更新已发哈希：%+v", hashes)
	}
	if len(hashes) != len(keys)-1 {
		t.Fatalf("应保留 %d 个 key，实际 %d", len(keys)-1, len(hashes))
	}
}

// fakeCompactor：第一次把会话末尾一次性折叠成摘要，之后不再压缩。
type fakeCompactor struct{ used bool }

func (f *fakeCompactor) Plan(_ context.Context, _ string, _ *session.Core, _ bool) (*reqlog.CompactionView, map[string]any, error) {
	if f.used {
		return nil, nil, nil
	}
	f.used = true
	return &reqlog.CompactionView{
		Summary: session.Content{session.NewTextPart("摘要：之前聊过天气")},
		Upto:    1,
	}, nil, nil
}

// TestCompactionReinjectsSnapshotAndStatus：压缩后另写快照与状态，仍在本次请求里（09 9.5 验收）。
func TestCompactionReinjectsSnapshotAndStatus(t *testing.T) {
	db := tempStore(t)
	m := &recordModel{}
	a := newAgent(t, db, agentOpts{model: m})
	a.Compactor = &fakeCompactor{}
	a.Statuses = []inject.Reg{
		{
			Key: "agents_md", Owner: "instructions",
			Fn: func(*callctx.Ctx) []inject.Item {
				return []inject.Item{{Key: "agents_md", Kind: inject.KindSnapshot, Body: "AGENTS 基线"}}
			},
		},
		{
			Key: "todo", Owner: "todo",
			Fn: func(*callctx.Ctx) []inject.Item {
				return []inject.Item{{Body: "任务 1"}}
			},
		},
	}
	if _, err := a.Send(context.Background(), "hi"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	text := requestText(m.last())
	for _, want := range []string{"摘要：之前聊过天气", "AGENTS 基线", "<status-update>", "任务 1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("压缩后的请求缺 %q：\n%s", want, text)
		}
	}
	if got := injected(a, session.KindSnapshot); len(got) != 1 || !strings.Contains(got[0], "AGENTS 基线") {
		t.Fatalf("压缩后应重新注入快照：%v", got)
	}
	if got := injected(a, session.KindStatus); len(got) != 1 {
		t.Fatalf("压缩后应重新注入状态：%v", got)
	}
	// 压缩条目已生效（回合结束后写入历史）。
	var sawCompaction bool
	for _, e := range a.Core.Context().Items {
		if e.Item.Kind == session.KindCompaction {
			sawCompaction = true
		}
	}
	if !sawCompaction {
		t.Fatal("回合结束后压缩条目应已写入历史")
	}
}

// TestReplayRequestPrefixStable：重放同一会话得到相同请求（缓存安全）。
func TestReplayRequestPrefixStable(t *testing.T) {
	db := tempStore(t)
	a := newAgent(t, db, agentOpts{model: &recordModel{}})
	a.Statuses = []inject.Reg{{
		Key: "todo", Owner: "todo",
		Fn: func(*callctx.Ctx) []inject.Item {
			return []inject.Item{{Body: "任务 1"}}
		},
	}}
	if _, err := a.Send(context.Background(), "hi"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	r1, err := session.BuildRequest(a.Core.Context())
	if err != nil {
		t.Fatalf("BuildRequest 1: %v", err)
	}
	r2, err := session.BuildRequest(a.Core.Context())
	if err != nil {
		t.Fatalf("BuildRequest 2: %v", err)
	}
	h1, err := session.RequestHash(r1)
	if err != nil {
		t.Fatalf("RequestHash 1: %v", err)
	}
	h2, err := session.RequestHash(r2)
	if err != nil {
		t.Fatalf("RequestHash 2: %v", err)
	}
	if h1 != h2 {
		t.Fatal("重放同一会话得到的请求不一致（缓存前缀不稳定）")
	}
}

// toolThenTextModel：第一次调模型走 echo 工具，第二次直接回文本（不依赖末条消息角色，
// 便于在工具结果后还追加了状态注入的场景下推进回合）。
type toolThenTextModel struct {
	mu    sync.Mutex
	calls int
}

func (m *toolThenTextModel) Stream(_ context.Context, req session.Request, onText func(string)) (session.AssistantItem, session.StopReason, error) {
	m.mu.Lock()
	m.calls++
	n := m.calls
	m.mu.Unlock()
	if n%2 == 1 {
		return session.AssistantItem{ToolCalls: []session.ToolCall{{WireID: "call_1", Name: "echo", Args: json.RawMessage(`{"s":"pong"}`)}}},
			session.StopReason{V: "ToolUse"}, nil
	}
	text := "工具说：pong"
	if onText != nil {
		onText(text)
	}
	return session.AssistantItem{Content: session.Content{session.NewTextPart(text)}}, session.StopReason{V: "EndTurn"}, nil
}
