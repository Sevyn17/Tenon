package host

import (
	"context"
	"testing"

	"github.com/Sevyn17/tenon/runtime/agent"
	"github.com/Sevyn17/tenon/runtime/mail"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
	"github.com/Sevyn17/tenon/runtime/store/memstore"
)

// L17：Send 带的 trace 随 inbox 持久化，回合开始时用它设 trace。
// 文件级关库重开由存储实现方验证；这里断言接口读写往返，以及回合用上了 inbox 里的 trace。
func TestInboxTracePersistsAndApplies(t *testing.T) {
	ctx := context.Background()
	db := memstore.New()
	t.Cleanup(func() { db.Close() })
	h := &Host{DB: db}
	h.New = func(id, def string) (*agent.Agent, *session.Config, error) {
		a := &agent.Agent{Store: db, SessionID: id, Model: &scriptedModel{}}
		return a, &session.Config{Model: "m"}, nil
	}
	hd := mustOpen(t, h, "child")

	if err := hd.Do(Command{Kind: Send, ItemID: NewItemID(), Text: "做点事",
		Source: "agent", Trace: "parent-tr"}); err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	if got := hd.Agent().TraceID(); got != "parent-tr" {
		t.Fatalf("回合 trace 应为 parent-tr，得 %q", got)
	}

	if fresh, err := db.Admit(ctx, "child", store.InboxItem{ItemID: "probe", Kind: store.InboxInput,
		Input: `[{"kind":"text","text":"x"}]`, Trace: "roundtrip"}); err != nil || !fresh {
		t.Fatalf("Admit：%v fresh=%v", err, fresh)
	}
	got, err := db.Inbox(ctx, "child")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range got {
		if it.ItemID == "probe" {
			found = true
			if it.Trace != "roundtrip" {
				t.Fatalf("inbox 项的 trace 应原样读回，得 %q", it.Trace)
			}
		}
	}
	if !found {
		t.Fatal("未读回 probe 项")
	}

	// 换一个宿主打开同一份存储，再投一条带 trace 的，确认回合仍用 inbox 里的 trace。
	h2 := &Host{DB: db}
	h2.New = h.New
	hd2 := mustOpen(t, h2, "child")
	if err := hd2.Do(Command{Kind: Send, ItemID: NewItemID(), Text: "再来", Trace: "parent-tr-2"}); err != nil {
		t.Fatal(err)
	}
	if err := hd2.Wait(); err != nil {
		t.Fatal(err)
	}
	if got := hd2.Agent().TraceID(); got != "parent-tr-2" {
		t.Fatalf("换宿主后回合 trace 应为 parent-tr-2，得 %q", got)
	}
}

// L17：不带 trace 的普通 Send 行为不变（回合 trace 仍置空）。
func TestPlainSendStillClearsTrace(t *testing.T) {
	h, _ := newExplainRig(t, &scriptedModel{})
	hd := mustOpen(t, h, "s1")

	if err := hd.Do(Command{Kind: Send, ItemID: NewItemID(), Text: "普通消息"}); err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	if got := hd.Agent().TraceID(); got != "" {
		t.Fatalf("普通 Send 的回合 trace 应为空，得 %q", got)
	}
}

// L17 端到端：父会话经 spawn 派子会话，子会话的事件记在父 trace 下。
func TestSpawnInheritsParentTrace(t *testing.T) {
	ctx := context.Background()
	h, db := newExplainRig(t, &scriptedModel{})

	phd := mustOpen(t, h, "parent")
	if err := h.deliverQueued(ctx, mail.Envelope{Trace: "tr-parent",
		From: mail.Address{Kind: mail.AddrHuman, ID: "u1"},
		To:   mail.Address{Kind: mail.AddrSession, ID: "parent"},
		Kind: mail.KindRequest, Body: "起个子任务"}); err != nil {
		t.Fatal(err)
	}
	if err := phd.Wait(); err != nil {
		t.Fatal(err)
	}
	if got := phd.Agent().TraceID(); got != "tr-parent" {
		t.Fatalf("父回合 trace 应为 tr-parent，得 %q", got)
	}

	if _, err := h.Spawn(ctx, "parent", "parent/sub-1", "", h.New, "子任务"); err != nil {
		t.Fatal(err)
	}

	evs, err := db.TraceEvents(ctx, "tr-parent")
	if err != nil {
		t.Fatal(err)
	}
	seenChild := false
	for _, ev := range evs {
		if ev.Session == "parent/sub-1" {
			seenChild = true
		}
	}
	if !seenChild {
		t.Fatalf("父 trace 下应有子会话事件，实际事件：%+v", evs)
	}
}
