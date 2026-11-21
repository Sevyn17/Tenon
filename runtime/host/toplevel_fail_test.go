package host

// 顶层会话回合失败后 actor 不退出（2026-11-21）：下一条 Send 直接开新回合；失败不自激重跑；
// 子会话（有 Origin）的失败上报不变。

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/runtime/modelhub"
	"github.com/Sevyn17/tenon/runtime/session"
)

// authModel 前 fail 次返回 AUTH（模拟 401），之后成功；fail < 0 一直失败。
type authModel struct {
	mu    sync.Mutex
	calls int
	fail  int
}

func (m *authModel) Stream(context.Context, session.Request, func(string)) (session.AssistantItem, session.StopReason, error) {
	m.mu.Lock()
	n := m.calls
	m.calls++
	m.mu.Unlock()
	if m.fail < 0 || n < m.fail {
		return session.AssistantItem{}, session.StopReason{}, &modelhub.Failure{Code: modelhub.CodeAuth, Message: "401 bad key"}
	}
	return session.AssistantItem{Content: session.Content{session.NewTextPart("ok")}}, session.StopReason{V: "stop"}, nil
}

func (m *authModel) n() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// sendWait 发一条用户消息并等会话空闲，返回 Wait 交回的错误。
func sendWait(t *testing.T, hd Handle, text string) error {
	t.Helper()
	if err := hd.Do(Command{Kind: Send, ItemID: NewItemID(), Text: text, Source: "user", Trust: "trusted"}); err != nil {
		t.Fatalf("发送 %q：%v", text, err)
	}
	done := make(chan error, 1)
	go func() { done <- hd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatalf("发送 %q 后 Wait 超时", text)
		return nil
	}
}

func alive(t *testing.T, hd Handle) {
	t.Helper()
	select {
	case <-hd.Done():
		t.Fatalf("顶层会话回合失败后 actor 不应退出：%v", hd.Err())
	default:
	}
}

// 顶层会话 401 后再 send：同一个 actor 开新回合并成功；失败原因照样经 Wait 与 last_end 报出。
func TestTopLevelAuthFailureThenSendSucceeds(t *testing.T) {
	ctx := context.Background()
	m := &authModel{fail: 1}
	r := newT2Rig(t, nil, m, 0, nil)
	hd := mustOpen(t, r.h, "s1")
	err := sendWait(t, hd, "第一条")
	if err == nil || !strings.Contains(err.Error(), "AUTH") {
		t.Fatalf("Wait 应交回 AUTH 失败：%v", err)
	}
	alive(t, hd)
	l, lerr := r.db.Load(ctx, "s1", nil)
	if lerr != nil {
		t.Fatal(lerr)
	}
	if l.RunState != "idle" || l.LastEnd == nil || l.LastEnd.End.Kind != session.EndFailed || l.LastEnd.End.Code != "AUTH" {
		t.Fatalf("失败应收尾为 idle 并记 last_end=Failed(AUTH)：run=%q last_end=%+v", l.RunState, l.LastEnd)
	}
	if hd2, ok := r.h.Get("s1"); !ok || hd2.a != hd.a {
		t.Fatal("失败后应仍是同一个打开着的 actor")
	}
	if err := sendWait(t, hd, "第二条"); err != nil {
		t.Fatalf("第二条应成功（上一轮的失败不再交给 Wait）：%v", err)
	}
	alive(t, hd)
	if got := m.n(); got != 2 {
		t.Fatalf("模型调用 %d 次，应为 2（失败 1 次 + 新回合 1 次）", got)
	}
	l, _ = r.db.Load(ctx, "s1", nil)
	if l.LastEnd == nil || l.LastEnd.End.Kind != session.EndDone || l.LastEnd.Turn != 2 {
		t.Fatalf("第二回合应以 Done 收尾：%+v", l.LastEnd)
	}
}

// 连续失败不自激：每条输入只跑一轮；空闲时不重跑；换宿主重开（模拟进程重启）也不续跑失败的回合。
func TestTopLevelFailureDoesNotLoop(t *testing.T) {
	ctx := context.Background()
	m := &authModel{fail: -1}
	r := newT2Rig(t, nil, m, 0, nil)
	hd := mustOpen(t, r.h, "s1")
	for i := 1; i <= 3; i++ {
		if err := sendWait(t, hd, "再试"); err == nil {
			t.Fatalf("第 %d 次应失败", i)
		}
		alive(t, hd)
		if got := m.n(); got != i {
			t.Fatalf("第 %d 条输入后模型调用 %d 次，应为 %d", i, got, i)
		}
	}
	time.Sleep(300 * time.Millisecond)
	if got := m.n(); got != 3 {
		t.Fatalf("空闲期间不应自己重跑：模型调用 %d 次", got)
	}
	items, err := r.db.Inbox(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.Kind != "note" {
			t.Fatalf("失败回合的输入应已消费，信箱不应留输入：%+v", items)
		}
	}
	// 进程重启：关掉本宿主，换一个宿主打开同一份存储。失败的一轮已收尾成 Idle，不应续跑。
	if err := hd.Do(Command{Kind: Drain}); err != nil {
		t.Fatal(err)
	}
	<-hd.Done()
	m2 := &authModel{fail: -1}
	r2 := newT2Rig(t, r.db, m2, 0, nil)
	hd2 := mustOpen(t, r2.h, "s1")
	if err := hd2.Wait(); err != nil {
		t.Fatalf("重开后空闲 Wait 不应带上次进程的失败：%v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if got := m2.n(); got != 0 {
		t.Fatalf("重开后不应续跑失败的回合：模型调用 %d 次", got)
	}
	alive(t, hd2)
}

// 子会话（origin=subagent）失败：错误随 Spawn 交回，actor 照旧退出（give_up 行为不变）。
func TestChildFailureStillExits(t *testing.T) {
	ctx := context.Background()
	m := &authModel{fail: -1}
	r := newT2Rig(t, nil, m, 0, nil)
	_, err := r.h.Spawn(ctx, "p", "p/sub-1", "", r.h.New, "任务")
	if err == nil || !strings.Contains(err.Error(), "AUTH") {
		t.Fatalf("子会话失败应交回 AUTH 错误：%v", err)
	}
	waitFor(t, func() bool { _, ok := r.h.Get("p/sub-1"); return !ok })
	if got := m.n(); got != 1 {
		t.Fatalf("give_up 不应重跑：模型调用 %d 次", got)
	}
}
