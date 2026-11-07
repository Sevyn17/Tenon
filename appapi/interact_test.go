package appapi

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"tenon/appapi/wire"
)

// 交互（10.6）：确认登记成交互，出现在快照与 interactions.list 里，打开 / 结束都是持久事件；
// 只收第一条回答，同一 client_id 重发返回第一次的结果，别的回答 conflict（answered）；不在 responders 里 forbidden。
func TestInteractionRespondFlow(t *testing.T) {
	app := newApp(t)
	if _, e := call2(t, app, "session.create", `{"session":"s"}`); e != nil {
		t.Fatal(e)
	}
	c := app.NewConn(context.Background(), LocalPrincipal)
	defer c.Close()
	if err := c.Subscribe(context.Background(), wire.SubscribeArgs{Session: "s"}); err != nil {
		t.Fatal(err)
	}
	waitKind(t, c, wire.EvSynced)
	if _, e := call2(t, app, "session.send", `{"session":"s","text":"call tool","client_id":"c1"}`); e != nil {
		t.Fatal(e)
	}
	opened := waitKind(t, c, wire.EvInteractionOpen)
	ev := opened[len(opened)-1]
	it, ok := ev.Data.(wire.Interaction)
	if !ok || ev.Cursor == 0 || it.ID == "" || it.Kind != wire.InteractionApproval || it.Approval == nil || it.Approval.Tool != "echo" {
		t.Fatalf("interaction.opened 应为持久事件、带 approval：%+v", ev)
	}
	if b, _ := json.Marshal(ev.Data); json.Valid(b) && containsStr(string(b), "corr") {
		t.Fatalf("交互不应带 corr：%s", b)
	}

	snap, e := call2(t, app, "session.get", `{"session":"s"}`)
	if e != nil {
		t.Fatal(e)
	}
	if s := snap.(wire.SessionSnapshot); len(s.Interactions) != 1 || s.Interactions[0].ID != it.ID {
		t.Fatalf("快照应带未结束的交互：%+v", s.Interactions)
	}
	list, e := call2(t, app, "interactions.list", `{}`)
	if e != nil || len(list.(wire.InteractionsResult).Items) != 1 {
		t.Fatalf("interactions.list：%+v %v", list, e)
	}

	// 不在 responders 里：forbidden。
	other := app.NewConn(context.Background(), wire.Principal{ID: "bob", Role: wire.RoleOperator})
	defer other.Close()
	_, err := other.Call(context.Background(), "interaction.respond",
		json.RawMessage(`{"interaction":"`+it.ID+`","response":{"approve":true}}`))
	wantCode(t, AsError(err), wire.CodeForbidden, "")

	args := `{"interaction":"` + it.ID + `","response":{"approve":true},"client_id":"r1"}`
	out, err := c.Call(context.Background(), "interaction.respond", json.RawMessage(args))
	if err != nil || out.(wire.RespondResult).Outcome != wire.OutcomeApproved {
		t.Fatalf("首次回答：%+v %v", out, err)
	}
	// 同一 client_id 重发：返回第一次的结果。
	out, err = c.Call(context.Background(), "interaction.respond", json.RawMessage(args))
	if err != nil || out.(wire.RespondResult).Outcome != wire.OutcomeApproved {
		t.Fatalf("重发应返回第一次的结果：%+v %v", out, err)
	}
	// 别的回答：conflict（answered），details 带已有 outcome。
	_, err = c.Call(context.Background(), "interaction.respond",
		json.RawMessage(`{"interaction":"`+it.ID+`","response":{"approve":false},"client_id":"r2"}`))
	e2 := AsError(err)
	wantCode(t, e2, wire.CodeConflict, wire.ConflictAnswered)
	if e2.Details["outcome"] != wire.OutcomeApproved {
		t.Fatalf("details.outcome 应为 approved：%+v", e2.Details)
	}

	resolved := waitKind(t, c, wire.EvInteractionDone)
	r := resolved[len(resolved)-1]
	if d, ok := r.Data.(wire.InteractionResolved); !ok || r.Cursor == 0 || d.ID != it.ID || d.Outcome != wire.OutcomeApproved || d.By != "local" {
		t.Fatalf("interaction.resolved：%+v", r)
	}
	waitKind(t, c, wire.EvTurnEnded)
	hd, _ := app.Host().Get("s")
	_ = hd.Wait()
	snap, _ = call2(t, app, "session.get", `{"session":"s"}`)
	if s := snap.(wire.SessionSnapshot); len(s.Interactions) != 0 {
		t.Fatalf("结束后快照不应再有交互：%+v", s.Interactions)
	}
	_, e = call2(t, app, "interaction.respond", `{"interaction":"nope","response":{"approve":true}}`)
	wantCode(t, e, wire.CodeNotFound, "")
}

// 回合取消时交互标为 voided（客户端不用猜）。
func TestInteractionVoidedOnCancel(t *testing.T) {
	app := newApp(t)
	if _, e := call2(t, app, "session.create", `{"session":"s"}`); e != nil {
		t.Fatal(e)
	}
	c := app.NewConn(context.Background(), LocalPrincipal)
	defer c.Close()
	if err := c.Subscribe(context.Background(), wire.SubscribeArgs{Session: "s"}); err != nil {
		t.Fatal(err)
	}
	waitKind(t, c, wire.EvSynced)
	if _, e := call2(t, app, "session.send", `{"session":"s","text":"call tool","client_id":"c1"}`); e != nil {
		t.Fatal(e)
	}
	waitKind(t, c, wire.EvInteractionOpen)
	if _, e := call2(t, app, "session.cancel", `{"session":"s"}`); e != nil {
		t.Fatal(e)
	}
	got := waitKind(t, c, wire.EvInteractionDone)
	if d := got[len(got)-1].Data.(wire.InteractionResolved); d.Outcome != wire.OutcomeVoided {
		t.Fatalf("取消后应 voided：%+v", d)
	}
	_ = time.Second
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
