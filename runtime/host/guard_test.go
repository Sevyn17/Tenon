package host

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/runtime/agent"
	"github.com/Sevyn17/tenon/runtime/bus"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/mail"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
)

// creatorRig 建一个创建者为 u1 的会话 s1。
func creatorRig(t *testing.T) *rig {
	t.Helper()
	r := newRig(t, nil, &fakeModel{})
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
	r.h.GuardDeliver = func(context.Context, component.GuardRequest) (component.Verdict, string) {
		return component.Deny, "测试屏蔽"
	}
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
	r.h.GuardDeliver = func(context.Context, component.GuardRequest) (component.Verdict, string) {
		return component.Allow, ""
	}
	if err := send(mail.Address{Kind: mail.AddrHuman, ID: "u2"}); err != nil {
		t.Fatalf("Allow 应放开 session 投递：%v", err)
	}
	// Allow 与 Deny 同时存在：Deny 胜。
	r.h.GuardDeliver = func(context.Context, component.GuardRequest) (component.Verdict, string) {
		return component.Deny, "仍要拦"
	}
	if err := send(mail.Address{Kind: mail.AddrHuman, ID: "u2"}); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("Deny 应胜过 Allow：%v", err)
	}
}

// TestGuardDeniedPersists：守卫拒绝同时写进组件事件日志（04 4.6，T4b）。
// 订阅者从序号 0 续读仍能读到，不依赖易失的总线。文件级关库重开由存储实现方验证。
func TestGuardDeniedPersists(t *testing.T) {
	ctx := context.Background()
	r := creatorRig(t)
	r.h.GuardDeliver = func(context.Context, component.GuardRequest) (component.Verdict, string) {
		return component.Deny, "测试屏蔽"
	}
	err := r.h.Deliver(ctx, mail.Envelope{
		From: mail.Address{Kind: mail.AddrHuman, ID: "u1"},
		To:   mail.Address{Kind: mail.AddrSession, ID: "s1"}, Body: "hi"})
	if !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("应被拒绝：%v", err)
	}

	got, err := r.db.(store.EventLog).Read(ctx, 0, 0)
	if err != nil || len(got) != 1 {
		t.Fatalf("应读到一条：%v %+v", err, got)
	}
	if got[0].Seq != 1 || got[0].Source != "guard" || got[0].Name != "guard.denied" {
		t.Fatalf("事件不对：%+v", got[0])
	}
	var data map[string]string
	if err := json.Unmarshal(got[0].Data, &data); err != nil {
		t.Fatal(err)
	}
	if data["reason"] != "测试屏蔽" || data["to"] != "session:s1" {
		t.Fatalf("载荷不对：%v", data)
	}
}

// TestGuardRuleSessionOpen：新建会话前的守卫（08 8.1）——Deny 时不建会话。
func TestGuardRuleSessionOpen(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, nil, &fakeModel{})
	r.h.GuardSessionOpen = func(_ context.Context, req component.GuardRequest) (component.Verdict, string) {
		if req.Agent == "worker" {
			return component.Deny, "不允许新建 worker"
		}
		return component.Abstain, ""
	}
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
