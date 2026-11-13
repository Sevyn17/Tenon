package host

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Sevyn17/tenon/modules/storage/sqlite"
	"github.com/Sevyn17/tenon/runtime/agent"
	"github.com/Sevyn17/tenon/runtime/mail"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
)

// L17：Send 带的 trace 要随 inbox 一起持久化，回合开始时用它设 trace。
// 这是「子会话事件记在父 trace 下」的基础。
func TestInboxTracePersistsAndApplies(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := sqlite.Open(ctx, filepath.Join(dir, "state.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	h := &Host{DB: db}
	h.New = func(id, def string) (*agent.Agent, *session.Config, error) {
		a := &agent.Agent{Store: db, SessionID: id, Model: &scriptedModel{}}
		return a, &session.Config{Model: "m"}, nil
	}
	hd := mustOpen(t, h, "child")

	// 模拟 spawn：投任务时带上父 trace。
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

	// 关库重开，模拟进程重启：trace 必须还在（它存在 inbox 表里）。
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db2, err := sqlite.Open(ctx, filepath.Join(dir, "state.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db2.Close() })
	h2 := &Host{DB: db2}
	h2.New = func(id, def string) (*agent.Agent, *session.Config, error) {
		a := &agent.Agent{Store: db2, SessionID: id, Model: &scriptedModel{}}
		return a, &session.Config{Model: "m"}, nil
	}
	hd2 := mustOpen(t, h2, "child")

	// 再投一条带 trace 的，验证重开后的库能正常读写 trace 列。
	if err := hd2.Do(Command{Kind: Send, ItemID: NewItemID(), Text: "再来", Trace: "parent-tr-2"}); err != nil {
		t.Fatal(err)
	}
	if err := hd2.Wait(); err != nil {
		t.Fatal(err)
	}
	if got := hd2.Agent().TraceID(); got != "parent-tr-2" {
		t.Fatalf("重开库后回合 trace 应为 parent-tr-2，得 %q", got)
	}

	// 读写往返：Admit 带 trace，Inbox 原样读回。
	if fresh, err := db2.Admit(ctx, "child", store.InboxItem{ItemID: "probe", Kind: store.InboxInput,
		Input: `[{"kind":"text","text":"x"}]`, Trace: "roundtrip"}); err != nil || !fresh {
		t.Fatalf("Admit：%v fresh=%v", err, fresh)
	}
	got, err := db2.Inbox(ctx, "child")
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

	// 父会话先跑一个带 trace 的回合（模拟「人类发起任务，链上有 trace」）。
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

	// 派子会话：Spawn 内部会用父会话当前 trace。
	if _, err := h.Spawn(ctx, "parent", "parent/sub-1", "", h.New, "子任务"); err != nil {
		t.Fatal(err)
	}

	// 父 trace 下应能看到子会话的事件。
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
