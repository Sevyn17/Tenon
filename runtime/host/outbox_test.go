package host

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"tenon/runtime/mail"
	"tenon/runtime/store"
)

// 回合提交后 actor 自己派发发件箱：发给本会话的 control（close / interrupt）不能在 actor 协程里
// 同步等自己受理命令（旧行为：FlushOutbox → control → hd.Do(Close) 等自己，死锁）。
func TestOutboxControlToSelfDoesNotDeadlock(t *testing.T) {
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	hd := mustOpen(t, r.h, "s1")
	env := mail.Envelope{From: mail.Address{Kind: mail.AddrComponent, ID: "test"},
		To: mail.Address{Kind: mail.AddrSession, ID: "s1"}, Kind: mail.KindControl, Body: "close"}
	if err := env.Normalize(); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(env)
	hd.Agent().EnqueueOutbox(env.ID, b)
	if err := hd.Do(Command{Kind: Send, Text: "你好"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-hd.Done(): // close 生效：actor 退出
	case <-time.After(5 * time.Second):
		t.Fatal("actor 卡在派发发件箱里（control 等自己受理命令）")
	}
}

// C6：永久失败的毒消息记死信、不再重试，不会堵住后面的信（旧实现：不支持的地址被当成临时错误，
// 每次派发都重试；发件箱每次只取 64 条，毒消息够多时正常的信永远派不出去）。
func TestOutboxPoisonDeadLettered(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	hd := mustOpen(t, r.h, "s1")
	mustOpen(t, r.h, "s2")
	put := func(env mail.Envelope) {
		t.Helper()
		if err := env.Normalize(); err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(env)
		hd.Agent().EnqueueOutbox(env.ID, b)
	}
	for i := 0; i < OutboxBatch+5; i++ { // 一批以上的毒消息：发给还不支持的 component: 地址
		put(mail.Envelope{From: mail.Address{Kind: mail.AddrComponent, ID: "t"}, To: mail.Address{Kind: mail.AddrComponent, ID: "x"}, Body: "poison"})
	}
	put(mail.Envelope{From: mail.Address{Kind: mail.AddrComponent, ID: "t"}, To: mail.Address{Kind: mail.AddrSession, ID: "s2"},
		Kind: mail.KindNotify, Mode: mail.ModeContext, Body: "正常的信"})
	if err := hd.Agent().Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := r.h.FlushOutbox(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if items, _ := r.db.Outbox(ctx, 0); len(items) != 0 {
		t.Fatalf("毒消息应记死信、正常的信应已派发，剩 %d 条", len(items))
	}
	inbox, _ := r.db.Inbox(ctx, "s2")
	if len(inbox) != 1 {
		t.Fatalf("正常的信应送到 s2：%+v", inbox)
	}
}

// C7：agent: 投递被联系名单拒绝时不留下会话（旧实现先建会话、开了 actor，再在投递时拒绝）。
func TestDeliverAgentRejectedLeavesNoSession(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	for i := 0; i < 3; i++ {
		err := r.h.Deliver(ctx, mail.Envelope{From: mail.Address{Kind: mail.AddrAgent, ID: "stranger"},
			To: mail.Address{Kind: mail.AddrAgent, ID: "worker"}, Body: "hi"})
		if !errors.Is(err, ErrNotAllowed) {
			t.Fatalf("agent 直投 agent: 应被联系名单拒绝，得 %v", err)
		}
	}
	if list, _ := r.db.ListSessions(ctx, 0, 100, false); len(list) != 0 {
		t.Fatalf("被拒的投递不应留下会话：%d 个", len(list))
	}
	r.h.mu.Lock()
	n := len(r.h.actors)
	r.h.mu.Unlock()
	if n != 0 {
		t.Fatalf("被拒的投递不应留下运行中的 actor：%d 个", n)
	}
}

// A9 / C6：两个宿主（两个进程）共用一个库同时派发，同一条外发只投递一次。
func TestOutboxTwoHostsDeliverOnce(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "s.db")
	r1 := newRig(t, path, &fakeModel{})
	r2 := newRig(t, path, &fakeModel{})
	hd := mustOpen(t, r1.h, "s1")
	env := mail.Envelope{From: mail.Address{Kind: mail.AddrComponent, ID: "test"},
		To: mail.Address{Kind: mail.AddrAgent, ID: "worker"}, Body: "任务"}
	if err := env.Normalize(); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(env)
	hd.Agent().EnqueueOutbox(env.ID, b)
	if err := hd.Agent().Commit(ctx); err != nil {
		t.Fatal(err)
	}
	before, _ := r1.db.ListSessions(ctx, 0, 100, false)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		for _, h := range []*Host{r1.h, r2.h} {
			wg.Add(1)
			go func(h *Host) { defer wg.Done(); _, _ = h.FlushOutbox(ctx) }(h)
		}
	}
	wg.Wait()
	after, _ := r1.db.ListSessions(ctx, 0, 100, false)
	if got := len(after) - len(before); got != 1 {
		t.Fatalf("两个宿主并发派发，同一条外发应只投递一次，新建了 %d 个会话", got)
	}
}

// 并发派发不重复投递：每个回合结束都会调 FlushOutbox，多个会话同时结束时
// 同一条外发只能投递一次（不带 key 的 agent: 投递每次都新建会话，重复即可见）。
func TestOutboxConcurrentFlushDeliversOnce(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	hd := mustOpen(t, r.h, "s1")
	env := mail.Envelope{From: mail.Address{Kind: mail.AddrComponent, ID: "test"},
		To: mail.Address{Kind: mail.AddrAgent, ID: "worker"}, Body: "任务"}
	if err := env.Normalize(); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(env)
	hd.Agent().EnqueueOutbox(env.ID, b)
	if err := hd.Agent().Commit(ctx); err != nil { // 只落库、不派发（不走回合）
		t.Fatal(err)
	}
	before, err := r.db.ListSessions(ctx, 0, 100, false)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = r.h.FlushOutbox(ctx)
		}()
	}
	wg.Wait()
	after, err := r.db.ListSessions(ctx, 0, 100, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(after) - len(before); got != 1 {
		t.Fatalf("同一条外发应只投递一次（新建 1 个会话），得 %d", got)
	}
	items, err := r.db.Outbox(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("派发后发件箱应为空：%+v", items)
	}
}

// D 组：宿主启动的存储清理会删掉过期的观测与记账行（已派发的外发、trace 事件），
// 不动未派发的外发。
func TestMaintainPrunesOldRows(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	hd := mustOpen(t, r.h, "s1")
	for _, id := range []string{"sent", "open"} {
		env := mail.Envelope{ID: id, From: mail.Address{Kind: mail.AddrComponent, ID: "t"},
			To: mail.Address{Kind: mail.AddrSession, ID: "nope"}, Body: id}
		_ = env.Normalize()
		b, _ := json.Marshal(env)
		hd.Agent().EnqueueOutbox(id, b)
	}
	if err := hd.Agent().Commit(ctx); err != nil {
		t.Fatal(err)
	}
	_ = r.db.MarkOutbox(ctx, "sent")
	_ = r.db.RecordTrace(ctx, store.TraceEvent{Trace: "old", Session: "s1", Kind: "k", At: 1})
	mctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { store.Maintain(mctx, store.MaintainOptions{Store: r.db, Every: time.Hour}); close(done) }() // Keep 缺省；先清一次
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done
	if evs, _ := r.db.TraceEvents(ctx, "old"); len(evs) != 0 {
		t.Fatalf("过期 trace 事件应清掉：%+v", evs)
	}
	items, _ := r.db.Outbox(ctx, 0)
	if len(items) != 1 || items[0].ID != "open" {
		t.Fatalf("未派发的外发不应清：%+v", items)
	}
}

// 不带会话键的 agent: 投递重投（发件箱至少一次：投递成功、标记前崩溃）只建一个会话，
// 信件也只进一次（2026-11-04 审查 P1-17：原先每次随机会话 id，重投就多建一个会话）。
func TestAgentDeliverRetryCreatesOneSession(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	env := mail.Envelope{From: mail.Address{Kind: mail.AddrComponent, ID: "test"},
		To: mail.Address{Kind: mail.AddrAgent, ID: "worker"}, Body: "任务"}
	if err := env.Normalize(); err != nil {
		t.Fatal(err)
	}
	before, _ := r.db.ListSessions(ctx, 0, 100, false)
	for i := 0; i < 2; i++ {
		if err := r.h.Deliver(ctx, env); err != nil {
			t.Fatalf("第 %d 次投递: %v", i+1, err)
		}
	}
	after, _ := r.db.ListSessions(ctx, 0, 100, false)
	if got := len(after) - len(before); got != 1 {
		t.Fatalf("同一封信重投应只建 1 个会话，建了 %d 个", got)
	}
}
