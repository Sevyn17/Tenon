package host

// 预算记账（T4f/T5c 基础设施）：回合结束把 usage 记到 budget_ref 账上；超限发 budget.exceeded 并挂起。

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/runtime/mail"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
)

// 记账：带 budget_ref 的信封触发一轮后，账上累计到该回合的 usage。
func TestBudgetAccountedPerTurn(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{usage: session.Usage{Input: 100, Output: 50}})
	hd, err := r.h.Open(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	_ = hd.Wait()
	if err := r.h.Deliver(ctx, mail.Envelope{
		From: mail.Address{Kind: mail.AddrHuman, ID: "u1"},
		To:   mail.Address{Kind: mail.AddrSession, ID: "s1"},
		Kind: mail.KindRequest, BudgetRef: "acct-1", Body: "hi",
	}); err != nil {
		t.Fatal(err)
	}
	_ = hd.Wait()
	bs := r.db
	deadline := time.Now().Add(5 * time.Second)
	for {
		u, err := bs.BudgetTotal(ctx, "acct-1")
		if err != nil {
			t.Fatal(err)
		}
		if u.Total() > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("账上应有本回合用量，实际 %v", u)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// 超限：上限很小，回合结束后发 budget.exceeded 并挂起会话。
func TestBudgetExceededSuspends(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{usage: session.Usage{Input: 100, Output: 50}})
	hd, err := r.h.Open(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	_ = hd.Wait()
	// 宿主级上限 100；先把账打到 1000，再跑一轮必然超限。
	r.h.DefaultBudget = 100
	bs := r.db
	if _, err := bs.AddBudget(ctx, "acct-2", store.BudgetUsage{Input: 1000}, 0); err != nil {
		t.Fatal(err)
	}
	if err := r.h.Deliver(ctx, mail.Envelope{
		From: mail.Address{Kind: mail.AddrHuman, ID: "u1"},
		To:   mail.Address{Kind: mail.AddrSession, ID: "s1"},
		Kind: mail.KindRequest, BudgetRef: "acct-2", Body: "hi",
	}); err != nil {
		t.Fatal(err)
	}
	_ = hd.Wait()
	// 会话应转入挂起（RunState = suspended）。
	deadline := time.Now().Add(5 * time.Second)
	for {
		l, err := r.db.Load(ctx, "s1", nil)
		if err != nil {
			t.Fatal(err)
		}
		if l.RunState == "suspended" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("超限后应挂起，RunState=%q", l.RunState)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
