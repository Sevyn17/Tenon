package host

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"ybase-kernel/core/mail"
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
