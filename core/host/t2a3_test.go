package host

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ybase-kernel/core/session"
	"ybase-kernel/core/store"
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
