package appapi

// 模型每次退避重试前发易失事件 model.retrying（客户端改进 M8，能力 model_retrying）。

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/runtime/modelhub"
)

// refusedAdapter 每次都像连接被拒一样失败（可重试的 TRANSPORT）。
type refusedAdapter struct{ calls atomic.Int32 }

func (*refusedAdapter) Protocol() string { return "refused" }

func (a *refusedAdapter) Stream(context.Context, *modelhub.Request, modelhub.Endpoint) <-chan modelhub.Event {
	a.calls.Add(1)
	ch := make(chan modelhub.Event, 1)
	ch <- modelhub.Event{Failed: &modelhub.Failure{Code: modelhub.CodeTransport, Message: "connection refused"}}
	close(ch)
	return ch
}

// 模型连续失败 N 次 → N−1 个 model.retrying（attempt 从 2 递增），最后 turn.ended failed。
func TestModelRetryingEvents(t *testing.T) {
	const n = 4 // retries = 3 → 共 4 次尝试
	f, err := modelhub.ParseCatalog(`
[provider.a]
protocol = "refused"
[model."a/m1"]
context_window = 1000
[alias.m]
plan = [{ model = "a/m1", retries = 3 }]
`)
	if err != nil {
		t.Fatal(err)
	}
	cat := modelhub.NewCatalog()
	if err := cat.Add(modelhub.LayerUser, "t", f); err != nil {
		t.Fatal(err)
	}
	pol := modelhub.DefaultPolicy
	pol.Initial, pol.MaxDelay = time.Millisecond, time.Millisecond
	hub := modelhub.New(cat, func(string) (string, error) { return "", nil }, &pol)
	ad := &refusedAdapter{}
	if err := hub.RegisterAdapter(ad); err != nil {
		t.Fatal(err)
	}
	app := newAppWith(t, &modelhub.Session{Hub: hub, SessionKey: "k"}, true)
	if _, e := call2(t, app, "session.create", `{"session":"r"}`); e != nil {
		t.Fatal(e)
	}
	c := subscribeLive(t, app, "r")
	if _, e := call2(t, app, "session.send", `{"session":"r","text":"hi","client_id":"c1"}`); e != nil {
		t.Fatal(e)
	}
	evs := waitKind(t, c, wire.EvTurnEnded)
	var got []wire.ModelRetrying
	for _, e := range evs {
		if e.Kind == wire.EvModelRetrying {
			got = append(got, e.Data.(wire.ModelRetrying))
			if e.Turn != 1 || e.Cursor != 0 {
				t.Fatalf("model.retrying 应带回合号、不带游标：%+v", e)
			}
		}
	}
	te := evs[len(evs)-1].Data.(wire.TurnEnded)
	if te.Kind != "failed" || ad.calls.Load() != n {
		t.Fatalf("应尝试 %d 次后 failed：%+v calls=%d", n, te, ad.calls.Load())
	}
	if len(got) != n-1 {
		t.Fatalf("应有 %d 个 model.retrying：%+v（%v）", n-1, got, kindsOf(evs))
	}
	for i, r := range got {
		if r.Attempt != i+2 || r.Max != n || r.WaitMs < 0 || r.Reason == "" {
			t.Fatalf("第 %d 个 model.retrying 不符：%+v", i, r)
		}
	}
}
