package host

// T6a 跨机器预算对齐：远端触发的回合不在接收方记账（08 8.4「记账按触发」）。
// 远端信封经 DeliverRemote 投递，from 归一成 component:remote。

import (
	"context"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/runtime/mail"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
)

// 远端带 budget_ref 投递后，接收方账上不应有这笔（记账归发送方）。
func TestRemoteTurnNotBudgetedLocally(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, nil, &fakeModel{usage: sessionUsage()})
	hd, err := r.h.Open(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	_ = hd.Wait()

	// 远端投一封带 budget_ref 的信。
	if err := r.h.DeliverRemote(ctx, mail.Envelope{
		From: mail.Address{Kind: mail.AddrComponent, ID: "peer"},
		To:   mail.Address{Kind: mail.AddrSession, ID: "s1"},
		Kind: mail.KindRequest, Body: "hello", BudgetRef: "shared-acct",
	}); err != nil {
		t.Fatal(err)
	}
	_ = hd.Wait()
	// 等一小会儿确保记账逻辑（若有）跑完。
	time.Sleep(200 * time.Millisecond)
	if u, err := r.db.(store.BudgetStore).BudgetTotal(ctx, "shared-acct"); err != nil {
		t.Fatal(err)
	} else if u.Total() != 0 {
		t.Fatalf("远端触发的回合不该在接收方记账，实际 %v", u)
	}
}

// 对照：本机用户触发（带 budget_ref）时接收方照常记账。
func TestLocalTurnStillBudgeted(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, nil, &fakeModel{usage: sessionUsage()})
	hd, err := r.h.Open(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	_ = hd.Wait()

	if err := r.h.Deliver(ctx, mail.Envelope{
		From: mail.Address{Kind: mail.AddrHuman, ID: "u1"},
		To:   mail.Address{Kind: mail.AddrSession, ID: "s1"},
		Kind: mail.KindRequest, Body: "hi", BudgetRef: "local-acct",
	}); err != nil {
		t.Fatal(err)
	}
	_ = hd.Wait()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if u, _ := r.db.(store.BudgetStore).BudgetTotal(ctx, "local-acct"); u.Total() > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("本机触发应记账")
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = store.Tenant
}

// sessionUsage 一个固定的 usage（测试记账用）。
func sessionUsage() session.Usage {
	return session.Usage{Input: 100, Output: 50}
}
