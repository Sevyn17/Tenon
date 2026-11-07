package host

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tenon/runtime/agent"
	"tenon/runtime/bus"
	"tenon/runtime/component"
	"tenon/runtime/mail"
	"tenon/runtime/session"
	"tenon/runtime/store"
)

// creatorRig 建一个创建者为 u1 的会话 s1。
func creatorRig(t *testing.T) *rig {
	t.Helper()
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	if _, err := r.h.open(context.Background(), "s1", "", r.h.New, func(ag *agent.Agent, cfg *session.Config) error {
		_, err := ag.OpenAs(context.Background(), cfg, store.SessionMeta{ID: "s1", Creator: "u1"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return r
}

// TestGuardRuleMerge：组件规则与内核缺省的合并（04 4.2、08 D4）——
// Deny 压过缺省允许；Allow 能放开「往 session: 投递」的缺省限制；拒绝写 guard.denied。
func TestGuardRuleMerge(t *testing.T) {
	ctx := context.Background()
	r := creatorRig(t)
	r.h.Bus = bus.New(0)
	sub := r.h.Bus.Subscribe(ctx, bus.Filter{Kinds: []string{"guard.denied"}})
	defer sub.Close()
	send := func(from mail.Address) error {
		return r.h.Deliver(ctx, mail.Envelope{From: from, To: mail.Address{Kind: mail.AddrSession, ID: "s1"}, Body: "hi"})
	}
	// 缺省当然允许创建者；Deny 规则应压过它，并带出规则给的拒绝原因。
	r.h.Guards = []component.GuardRule{{
		Name: "block", On: component.GuardDeliver,
		Check: func(component.GuardRequest) (component.Verdict, string) { return component.Deny, "测试屏蔽" },
	}}
	err := send(mail.Address{Kind: mail.AddrHuman, ID: "u1"})
	if !errors.Is(err, ErrNotAllowed) || !strings.Contains(err.Error(), "测试屏蔽") {
		t.Fatalf("Deny 应压过缺省允许：%v", err)
	}
	select {
	case e := <-sub.C():
		if e.Kind != "guard.denied" {
			t.Fatalf("应发布 guard.denied，得到 %s", e.Kind)
		}
	case <-time.After(time.Second):
		t.Fatal("应发布 guard.denied 事件")
	}
	// Allow 规则放开「往 session: 投递」的缺省联系限制（u2 不是创建者）。
	r.h.Guards = []component.GuardRule{{
		Name: "open", On: component.GuardDeliver,
		Check: func(component.GuardRequest) (component.Verdict, string) { return component.Allow, "" },
	}}
	if err := send(mail.Address{Kind: mail.AddrHuman, ID: "u2"}); err != nil {
		t.Fatalf("Allow 应放开 session 投递：%v", err)
	}
	// Allow 与 Deny 同时存在：Deny 胜。
	r.h.Guards = append(r.h.Guards, component.GuardRule{
		Name: "block2", On: component.GuardDeliver,
		Check: func(component.GuardRequest) (component.Verdict, string) { return component.Deny, "仍要拦" },
	})
	if err := send(mail.Address{Kind: mail.AddrHuman, ID: "u2"}); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("Deny 应胜过 Allow：%v", err)
	}
}

// TestGuardRuleSessionOpen：新建会话前的守卫（08 8.1）——Deny 时不建会话。
func TestGuardRuleSessionOpen(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	r.h.Guards = []component.GuardRule{{
		Name: "no-worker", On: component.GuardSessionOpen,
		Check: func(req component.GuardRequest) (component.Verdict, string) {
			if req.Agent == "worker" {
				return component.Deny, "不允许新建 worker"
			}
			return component.Abstain, ""
		},
	}}
	err := r.h.Deliver(ctx, mail.Envelope{
		From: mail.Address{Kind: mail.AddrHuman, ID: "u1"},
		To:   mail.Address{Kind: mail.AddrAgent, ID: "worker"},
		Key:  "k1", Body: "hi",
	})
	if !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("GuardSessionOpen Deny 应拒绝投递：%v", err)
	}
	if _, err := r.db.FindSessionByKey(ctx, "worker", "u1", "k1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("被拒绝时不应建会话：%v", err)
	}
}
