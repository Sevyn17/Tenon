package host

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"tenon/runtime/session"
	"tenon/runtime/store"
	"tenon/runtime/toolhub"
)

// failModel 可配置前 N 次调用失败（fail < 0 表示一直失败），用于所属树策略测试。
type failModel struct {
	mu    sync.Mutex
	calls int
	fail  int
}

func (m *failModel) Stream(context.Context, session.Request, func(string)) (session.AssistantItem, session.StopReason, error) {
	m.mu.Lock()
	n := m.calls
	m.calls++
	m.mu.Unlock()
	if m.fail < 0 || n < m.fail {
		return session.AssistantItem{}, session.StopReason{}, errors.New("model: 故障")
	}
	return session.AssistantItem{Content: session.Content{session.NewTextPart("ok")}}, session.StopReason{V: "stop"}, nil
}

func (m *failModel) getCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// TestTreePolicyGiveUpChild：缺省 give_up——子会话一轮失败，错误原样交回调用方，会话落库为 idle。
func TestTreePolicyGiveUpChild(t *testing.T) {
	ctx := context.Background()
	r := newT2Rig(t, filepath.Join(t.TempDir(), "s.db"), &failModel{fail: -1}, 0, nil)
	if _, err := r.h.Spawn(ctx, "p", "p/sub-1", "", r.h.New, "任务"); err == nil {
		t.Fatal("give_up：子会话失败应把错误交回调用方")
	}
	l, err := r.db.Load(ctx, "p/sub-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if l.Meta.Origin != "subagent" || l.Meta.ParentID != "p" {
		t.Fatalf("子会话行不对：%+v", l.Meta)
	}
	if l.RunState != "idle" {
		t.Fatalf("失败的一轮应收尾为 idle，得到 %q", l.RunState)
	}
	if l.Snapshot.Header.Status != session.StatusIdle {
		t.Fatalf("失败的一轮应回到 Idle，得到 %v", l.Snapshot.Header.Status)
	}
}

// TestTreePolicyRestartChild：restart 策略——子会话第一次失败后从检查点重跑，第二次成功。
func TestTreePolicyRestartChild(t *testing.T) {
	ctx := context.Background()
	m := &failModel{fail: 1}
	r := newT2Rig(t, filepath.Join(t.TempDir(), "s.db"), m, 0, nil)
	r.h.TreePolicy = "restart"
	r.h.TreeRestarts = 1
	text, err := r.h.Spawn(ctx, "p", "p/sub-1", "", r.h.New, "任务")
	if err != nil {
		t.Fatalf("restart：重跑后应成功，得到 %v（模型调用 %d 次）", err, m.getCalls())
	}
	if text != "ok" {
		t.Fatalf("结果 = %q，期望 ok", text)
	}
	if got := m.getCalls(); got != 2 {
		t.Fatalf("模型应被调 2 次（失败 1 次 + 重跑 1 次），得到 %d", got)
	}
}

// TestInterruptPublishesAndKeepsSession：中断把回合收尾成 Cancelled，会话仍可继续收信。
func TestInterruptPublishesAndKeepsSession(t *testing.T) {
	m := &scriptedModel{gate: make(chan struct{}), start: make(chan int, 8)}
	r := newT2Rig(t, filepath.Join(t.TempDir(), "s.db"), m, 0, nil)
	var mu sync.Mutex
	var ends []session.EndReason
	r.h.Hooks.AfterTurn = func(_ string, end session.EndReason) {
		mu.Lock()
		ends = append(ends, end)
		mu.Unlock()
	}
	hd := mustOpen(t, r.h, "s1")
	if err := hd.Do(Command{Kind: Send, ItemID: NewItemID(), Text: "一", Source: "user", Trust: "trusted"}); err != nil {
		t.Fatal(err)
	}
	waitStarted(t, m.start, "第一次模型调用")
	if err := hd.Do(Command{Kind: Cancel}); err != nil {
		t.Fatal(err)
	}
	close(m.gate)
	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(ends) > 0
	})
	mu.Lock()
	got := ends[0]
	mu.Unlock()
	if got.Kind != session.EndCancelled {
		t.Fatalf("中断后的结束原因 = %v，期望 Cancelled", got)
	}
	// 中断不关会话：actor 还在，能继续处理下一轮。
	if _, ok := r.h.Get("s1"); !ok {
		t.Fatal("中断后 actor 不应退出")
	}
	if err := hd.Do(Command{Kind: Send, ItemID: NewItemID(), Text: "二", Source: "user", Trust: "trusted"}); err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	if text := textOf(messages(t, hd)); !strings.Contains(text, "ok") {
		t.Fatalf("中断后应能继续完成一轮，消息：%s", text)
	}
}

// TestSuspendStopsTurnsUntilResume：预算挂起收下信件但不开回合，Resume 后处理。
func TestSuspendStopsTurnsUntilResume(t *testing.T) {
	ctx := context.Background()
	r := newT2Rig(t, filepath.Join(t.TempDir(), "s.db"), &scriptedModel{}, 0, nil)
	hd := mustOpen(t, r.h, "s1")
	if err := hd.Do(Command{Kind: Send, ItemID: NewItemID(), Text: "一", Source: "user", Trust: "trusted"}); err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := r.h.Suspend(ctx, "s1", "budget"); err != nil {
		t.Fatal(err)
	}
	if st, _ := r.db.SessionStatus(ctx, "s1"); st != store.StatusSuspended {
		t.Fatalf("状态 = %q，期望 suspended", st)
	}
	if err := hd.Do(Command{Kind: Send, ItemID: NewItemID(), Text: "二", Source: "user", Trust: "trusted"}); err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if text := textOf(messages(t, hd)); strings.Contains(text, "二") {
		t.Fatalf("挂起期间不应开回合，消息：%s", text)
	}
	if err := hd.Do(Command{Kind: Resume}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return strings.Contains(textOf(messages(t, hd)), "二") })
	if st, _ := r.db.SessionStatus(ctx, "s1"); st != store.StatusActive {
		t.Fatalf("Resume 后状态 = %q，期望 active", st)
	}
}

// C1：Suspend 在会话自己的 actor 里执行（不在调用方 goroutine 里改会话核心）；
// 回合进行中调用时等回合结束后再挂起。用 -race 跑能看出旧实现的数据竞争。
func TestSuspendRunsInActor(t *testing.T) {
	ctx := context.Background()
	m := &scriptedModel{start: make(chan int, 4), gate: make(chan struct{})}
	r := newT2Rig(t, filepath.Join(t.TempDir(), "s.db"), m, 0, nil)
	hd := mustOpen(t, r.h, "s1")
	if err := hd.Do(Command{Kind: Send, Text: "一"}); err != nil {
		t.Fatal(err)
	}
	waitStarted(t, m.start, "回合开始")
	done := make(chan error, 1)
	go func() { done <- r.h.Suspend(ctx, "s1", "budget") }() // 回合中：排到回合结束后
	// 并发改头部（回合中 SetExt 在步边界受理，模型阻塞时会一直排着，所以放后台）。
	exts := make(chan struct{})
	go func() {
		defer close(exts)
		for i := 0; i < 20; i++ {
			_ = hd.Do(Command{Kind: SetExt, Key: "app.k", Value: i})
		}
	}()
	close(m.gate)
	<-exts
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("回合结束后挂起应成功: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Suspend 没有在回合结束后执行")
	}
	if st, _ := r.db.SessionStatus(ctx, "s1"); st != store.StatusSuspended {
		t.Fatalf("状态 = %q，期望 suspended", st)
	}
}

// 信箱积压时回合中到达的 Suspend 在本回合结束后立即生效，不等信箱清空（审查 P1-15：原先 Suspend
// 只在 drainInbox 返回后才执行，积压的消息会一条条开回合，预算挂起照样超支）。
func TestSuspendDuringBacklog(t *testing.T) {
	ctx := context.Background()
	m := &scriptedModel{start: make(chan int, 8), gate: make(chan struct{})}
	r := newT2Rig(t, filepath.Join(t.TempDir(), "s.db"), m, 0, nil)
	hd := mustOpen(t, r.h, "s1")
	if err := hd.Do(Command{Kind: Send, Text: "一"}); err != nil {
		t.Fatal(err)
	}
	waitStarted(t, m.start, "第一回合开始")
	for _, s := range []string{"二", "三", "四"} { // 积压：回合进行中排进信箱
		if err := hd.Do(Command{Kind: Send, Text: s}); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan error, 1)
	go func() { done <- r.h.Suspend(ctx, "s1", "budget") }()
	waitFor(t, func() bool { return len(hd.a.in) > 0 }) // Suspend 已进队列（原先固定睡 50ms，负载高时不够）
	close(m.gate)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("挂起: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Suspend 没有在本回合结束后执行")
	}
	m.mu.Lock()
	calls := m.calls
	m.mu.Unlock()
	if calls > 2 { // 第一回合 1 次；中途插入可能把积压并进同一回合多调一次，但不能再开新回合
		t.Fatalf("挂起前不应再开新回合，模型被调用 %d 次", calls)
	}
	if st, _ := r.db.SessionStatus(ctx, "s1"); st != store.StatusSuspended {
		t.Fatalf("状态 = %q，期望 suspended", st)
	}
}

// 回合失败时，回合中插入、还没进模型的插话写回信箱（审查 P1-19：原先丢掉）。
// 第一次调模型要工具，工具阻塞期间插话；插话在下一次调模型前进内核暂存，第二次调模型失败。
func TestFailedTurnHandsBackInterjection(t *testing.T) {
	ctx := context.Background()
	m := &gatedFailModel{}
	gate, inTool := make(chan struct{}), make(chan struct{}, 1)
	r := newT2Rig(t, filepath.Join(t.TempDir(), "s.db"), m, 0, func(context.Context, json.RawMessage) (toolhub.Output, error) {
		inTool <- struct{}{}
		<-gate
		return toolhub.Text("echoed"), nil
	})
	hd := mustOpen(t, r.h, "s1")
	if err := hd.Do(Command{Kind: Send, Text: "一"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-inTool:
	case <-time.After(5 * time.Second):
		t.Fatal("工具没有开始")
	}
	steered := make(chan error, 1)
	go func() { steered <- hd.Do(Command{Kind: Steer, Text: "别忘了这个"}) }()
	waitFor(t, func() bool { return len(hd.a.in) > 0 }) // 插话已进队列，工具结束后的边界取到它
	close(gate)
	if err := <-steered; err != nil {
		t.Fatal(err)
	}
	_ = hd.Wait()
	if m.calls() != 2 {
		t.Fatalf("应调两次模型，得 %d", m.calls())
	}
	items, err := r.db.Inbox(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.Kind == store.InboxNote && strings.Contains(it.Input, "别忘了这个") {
			return
		}
	}
	t.Fatalf("插话应写回信箱：%+v", items)
}

// gatedFailModel 第一次调用要一次 echo 工具，之后一律失败。
type gatedFailModel struct {
	mu sync.Mutex
	n  int
}

func (m *gatedFailModel) calls() int { m.mu.Lock(); defer m.mu.Unlock(); return m.n }

func (m *gatedFailModel) Stream(ctx context.Context, req session.Request, onText func(string)) (session.AssistantItem, session.StopReason, error) {
	m.mu.Lock()
	m.n++
	n := m.n
	m.mu.Unlock()
	if n == 1 {
		return session.AssistantItem{ToolCalls: []session.ToolCall{{Name: "echo", Args: json.RawMessage(`{}`)}}}, session.StopReason{V: "tool_calls"}, nil
	}
	return session.AssistantItem{}, session.StopReason{}, errors.New("模型坏了")
}
