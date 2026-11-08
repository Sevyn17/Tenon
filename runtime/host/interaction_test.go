package host

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/runtime/bus"
	"github.com/Sevyn17/tenon/runtime/store"
)

// waitInteraction 等到 human_requested，返回其中的交互 id。
func waitInteraction(t *testing.T, sub *bus.Sub) string {
	t.Helper()
	select {
	case ev := <-sub.C():
		m, _ := ev.Data.(map[string]any)
		iid, _ := m["interaction"].(string)
		if iid == "" {
			t.Fatalf("human_requested 应带交互 id：%v", ev.Data)
		}
		return iid
	case <-time.After(5 * time.Second):
		t.Fatal("没收到 human_requested")
	}
	return ""
}

func eventKinds(t *testing.T, db store.Store, id string) map[string]int {
	t.Helper()
	evs, _, err := db.Replay(context.Background(), id, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for _, e := range evs {
		out[e.Kind]++
	}
	return out
}

// 工具确认登记成 approval 交互（06 10.6）：打开、结束都是持久事件；Respond 只收第一条，之后的返回已有结果。
func TestApprovalInteractionRespond(t *testing.T) {
	h, db := newHumanRig(t, &scriptedModel{firstTool: true})
	ctx := context.Background()
	sub := h.Bus.Subscribe(ctx, bus.Filter{Kinds: []string{"human_requested"}})
	hd := mustOpen(t, h, "s1")
	if err := hd.Do(Command{Kind: Send, ItemID: NewItemID(), Text: "跑工具"}); err != nil {
		t.Fatal(err)
	}
	iid := waitInteraction(t, sub)
	open, err := db.OpenInteractions(ctx, "s1")
	if err != nil || len(open) != 1 || open[0].InteractionID != iid || open[0].Kind != "approval" {
		t.Fatalf("应有一条未结束的 approval 交互：%+v %v", open, err)
	}
	p, won, err := h.Respond(ctx, iid, store.Resolution{Outcome: OutcomeApproved, By: "local", ClientID: "r1"}, Answer{Approve: true})
	if err != nil || !won || p.Outcome != OutcomeApproved {
		t.Fatalf("第一次回答应生效：%+v %v %v", p, won, err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	p2, won, err := h.Respond(ctx, iid, store.Resolution{Outcome: OutcomeDenied, By: "other"}, Answer{})
	if err != nil || won || p2.Outcome != OutcomeApproved || p2.By != "local" {
		t.Fatalf("第二次回答不生效、返回已有结果：%+v %v %v", p2, won, err)
	}
	k := eventKinds(t, db, "s1")
	if k[store.EventInteractionOpened] != 1 || k[store.EventInteractionResolved] != 1 {
		t.Fatalf("打开与结束各一条持久事件：%v", k)
	}
	if left, _ := db.OpenInteractions(ctx, "s1"); len(left) != 0 {
		t.Fatalf("回答后不应还有未结束的交互：%+v", left)
	}
}

// 回合取消：交互结束为 voided；不存在的交互 Respond 返回 ErrNotFound。
func TestApprovalInteractionVoidedOnCancel(t *testing.T) {
	h, db := newHumanRig(t, &scriptedModel{firstTool: true})
	ctx := context.Background()
	sub := h.Bus.Subscribe(ctx, bus.Filter{Kinds: []string{"human_requested"}})
	hd := mustOpen(t, h, "s1")
	if err := hd.Do(Command{Kind: Send, ItemID: NewItemID(), Text: "跑工具"}); err != nil {
		t.Fatal(err)
	}
	iid := waitInteraction(t, sub)
	if err := hd.Do(Command{Kind: Cancel}); err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	p, err := db.Interaction(ctx, iid)
	if err != nil || p.Outcome != OutcomeVoided {
		t.Fatalf("取消后交互应为 voided：%+v %v", p, err)
	}
	if _, _, err := h.Respond(ctx, "i-none", store.Resolution{Outcome: OutcomeApproved}, Answer{Approve: true}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("不存在的交互应 ErrNotFound：%v", err)
	}
}

// 上次进程留下的未结束交互在会话打开时标为 voided（10.6：进程重启后如实反映已失效）。
func TestStaleInteractionVoidedOnOpen(t *testing.T) {
	h, db := newHumanRig(t, &scriptedModel{firstTool: true})
	ctx := context.Background()
	if _, err := h.Create(ctx, "s1", "", nil); err != nil {
		t.Fatal(err)
	}
	hd, _ := h.Get("s1")
	if err := hd.Do(Command{Kind: Close}); err != nil {
		t.Fatal(err)
	}
	<-hd.Done()
	if err := db.OpenInteraction(ctx, store.Pending{Corr: "c-old", SessionID: "s1", InteractionID: "i-old", Kind: "approval"}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Open(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	if p, err := db.Interaction(ctx, "i-old"); err != nil || p.Outcome != OutcomeVoided {
		t.Fatalf("打开会话应把遗留交互标为 voided：%+v %v", p, err)
	}
}
