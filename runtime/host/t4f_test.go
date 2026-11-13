package host

// T4f：跳数上限可配、本轮触发者可中断本轮、不同权限主体分轮。

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/runtime/mail"
)

// 租户把跳数上限调小后，超限投递被拒（MaxHop 从 Host.MaxHop 取，0 = 内核缺省 8）。
func TestMaxHopConfigurable(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, nil, &fakeModel{})
	hd, err := r.h.Open(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	_ = hd.Wait()
	r.h.MaxHop = 2
	// Hop=2 的信再投一次 → 3 > 2，拒。
	err = r.h.deliverQueued(ctx, mail.Envelope{From: mail.Address{Kind: mail.AddrHuman, ID: "u1"},
		To: mail.Address{Kind: mail.AddrSession, ID: "s1"}, Kind: mail.KindRequest, Hop: 2, Body: "x"})
	if !errors.Is(err, ErrTooManyHops) {
		t.Fatalf("MaxHop=2 时 Hop=2 再投应超限：%v", err)
	}
	// 缺省（0）按 8：Hop=7 再投（=8）不超。
	r.h.MaxHop = 0
	var s2 = "s2"
	hd2, err := r.h.Open(ctx, s2)
	if err != nil {
		t.Fatal(err)
	}
	_ = hd2.Wait()
	if err := r.h.deliverQueued(ctx, mail.Envelope{From: mail.Address{Kind: mail.AddrHuman, ID: "u1"},
		To: mail.Address{Kind: mail.AddrSession, ID: s2}, Kind: mail.KindRequest, Hop: 7, Body: "x"}); err != nil {
		t.Fatalf("缺省 8 跳：Hop=7 再投应放行：%v", err)
	}
}

// 多人会话里，本轮触发者（非创建者）可中断本轮；别的用户不行（02 2.4，T4f）。
func TestControlByTurnTrigger(t *testing.T) {
	ctx := context.Background()
	m := &fakeModel{block: make(chan struct{})}
	r := newRig(t, nil, m)
	hd, err := r.h.Open(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	_ = hd.Wait()
	// 由 u2 触发一轮（阻塞在模型里）。
	admitEnvelope(t, r.db, "s1", mail.Envelope{From: mail.Address{Kind: mail.AddrHuman, ID: "u2"},
		To: mail.Address{Kind: mail.AddrSession, ID: "s1"}, Kind: mail.KindRequest, Body: "u2 起一轮"})
	if err := hd.Do(Command{Kind: Deliver}); err != nil {
		t.Fatal(err)
	}
	// 等这一轮真的开始（模型被调用、阻塞）。
	deadline := time.Now().Add(3 * time.Second)
	for {
		m.mu.Lock()
		calls := m.calls
		m.mu.Unlock()
		if calls >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("回合没开始")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// 非触发者 u3 发 control interrupt：应被拒。
	if err := r.h.Deliver(ctx, mail.Envelope{From: mail.Address{Kind: mail.AddrHuman, ID: "u3"},
		To: mail.Address{Kind: mail.AddrSession, ID: "s1"}, Kind: mail.KindControl, Mode: mail.ModeControl,
		Body: "interrupt"}); err == nil {
		t.Fatal("非触发者的 control 应被拒")
	}
	// 触发者 u2 发 control interrupt：应放行（中断本轮，解除模型阻塞）。
	if err := r.h.Deliver(ctx, mail.Envelope{From: mail.Address{Kind: mail.AddrHuman, ID: "u2"},
		To: mail.Address{Kind: mail.AddrSession, ID: "s1"}, Kind: mail.KindControl, Mode: mail.ModeControl,
		Body: "interrupt"}); err != nil {
		t.Fatalf("本轮触发者应能中断本轮：%v", err)
	}
	close(m.block)
	_ = hd.Wait()
}
