package appapi

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/modules/storage/sqlite"
	"github.com/Sevyn17/tenon/runtime/agent"
	"github.com/Sevyn17/tenon/runtime/bus"
	"github.com/Sevyn17/tenon/runtime/host"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// waitKind 从连接取事件直到出现 kind；返回途中收到的全部事件（含它）。
func waitKind(t *testing.T, c *Conn, kind string) []wire.Envelope {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var got []wire.Envelope
	for {
		evs, err := c.Next(ctx)
		if err != nil {
			t.Fatalf("等 %s：%v（已收到 %d 条）", kind, err, len(got))
		}
		for i, e := range evs {
			got = append(got, e)
			if e.Kind == kind {
				if rest := evs[i+1:]; len(rest) > 0 { // 没取完的放回去，下一次 waitKind 接着读
					c.mu.Lock()
					c.queue = append(append([]wire.Envelope(nil), rest...), c.queue...)
					c.signalLocked()
					c.mu.Unlock()
				}
				return got
			}
		}
	}
}

func kindsOf(evs []wire.Envelope) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.Kind
	}
	return out
}

func sendWait(t *testing.T, app *App, id, text, cid string) {
	t.Helper()
	if _, e := call2(t, app, "session.send", `{"session":"`+id+`","text":"`+text+`","client_id":"`+cid+`"}`); e != nil {
		t.Fatal(e)
	}
	hd, _ := app.Host().Get(id)
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
}

// 回放 → synced → 实时；持久事件带递增的 cursor，回合结束是结构化的 turn.ended。
func TestConnSubscribeReplayThenLive(t *testing.T) {
	app := newApp(t)
	if _, e := call2(t, app, "session.create", `{"session":"s"}`); e != nil {
		t.Fatal(e)
	}
	sendWait(t, app, "s", "a", "c1")
	c := app.NewConn(context.Background(), LocalPrincipal)
	defer c.Close()
	zero := int64(0)
	if err := c.Subscribe(context.Background(), wire.SubscribeArgs{Session: "s", After: &zero}); err != nil {
		t.Fatal(err)
	}
	got := waitKind(t, c, wire.EvSynced)
	var last int64
	entries := 0
	for _, e := range got {
		if e.Cursor > 0 {
			if e.Cursor <= last {
				t.Fatalf("游标应递增：%v", kindsOf(got))
			}
			last = e.Cursor
		}
		if e.Kind == wire.EvEntryAppended {
			entries++
		}
		if !wire.Public(e.Kind) {
			t.Fatalf("不应推内部事件 %s", e.Kind)
		}
	}
	if entries != 2 {
		t.Fatalf("回放应有 2 条条目：%v", kindsOf(got))
	}
	if s := got[len(got)-1].Data.(wire.CursorRef); s.Cursor != last {
		t.Fatalf("synced 的 cursor 应为最后一条持久事件 %d：%d", last, s.Cursor)
	}
	sendWait(t, app, "s", "b", "c2")
	live := waitKind(t, c, wire.EvTurnEnded)
	te := live[len(live)-1].Data.(wire.TurnEnded)
	if te.Kind != "done" || te.Turn != 2 {
		t.Fatalf("turn.ended 应结构化：%+v", te)
	}
	n := 0
	for _, e := range live {
		if e.Kind == wire.EvEntryAppended {
			n++
			if e.Cursor <= last {
				t.Fatalf("实时条目游标应接在回放之后：%d ≤ %d", e.Cursor, last)
			}
			last = e.Cursor
		}
	}
	if n != 2 {
		t.Fatalf("实时应收到 2 条条目：%v", kindsOf(live))
	}

	// 断线重连：从最后收到的游标续订，不漏不重。
	c2 := app.NewConn(context.Background(), LocalPrincipal)
	defer c2.Close()
	mid := last - 1
	if err := c2.Subscribe(context.Background(), wire.SubscribeArgs{Session: "s", After: &mid}); err != nil {
		t.Fatal(err)
	}
	re := waitKind(t, c2, wire.EvSynced)
	// 续订回放 cursor > mid 的全部持久事件，连续不缺不重：第一条是最后一条条目（last），
	// 之后可能还有同一检查点写的运行状态事件。
	want := mid + 1
	for _, e := range re {
		if e.Cursor == 0 {
			continue
		}
		if e.Cursor != want {
			t.Fatalf("续订回放应从 %d 起连续：得到 %d（%v）", want, e.Cursor, kindsOf(re))
		}
		want++
	}
	if want == mid+1 {
		t.Fatalf("续订应回放最后一条：%v", kindsOf(re))
	}
	if first := re[0]; first.Kind != wire.EvEntryAppended || first.Cursor != last {
		t.Fatalf("续订的第一条应是游标 %d 的条目：%s %d", last, first.Kind, first.Cursor)
	}
}

// viewer 只能查询和订阅（10.8）。
func TestConnViewerForbidden(t *testing.T) {
	app := newApp(t)
	c := app.NewConn(context.Background(), wire.Principal{ID: "v", Role: wire.RoleViewer})
	defer c.Close()
	_, err := c.Call(context.Background(), "session.create", json.RawMessage(`{"session":"x"}`))
	if e := AsError(err); e == nil || e.Code != wire.CodeForbidden {
		t.Fatalf("viewer 建会话应 forbidden：%v", err)
	}
	out, err := c.Call(context.Background(), "hello", nil)
	if err != nil || out.(wire.HelloResult).Principal.Role != wire.RoleViewer {
		t.Fatalf("hello 应返回连接身份：%v %v", out, err)
	}
}

// 跨进程（10.2.4）：另一个进程（同一库文件上的另一个 Host、另一条总线）写入的事件靠按游标轮询补齐。
func TestConnPollsOtherProcess(t *testing.T) {
	old := PollInterval
	PollInterval = 50 * time.Millisecond
	defer func() { PollInterval = old }()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "s.db")
	open := func() (*sqlite.DB, *bus.Bus, *host.Host) {
		db, err := sqlite.Open(ctx, path, sqlite.Options{})
		if err != nil {
			t.Fatal(err)
		}
		b := bus.New(0)
		t.Cleanup(func() { b.Close(); db.Close() })
		h := &host.Host{DB: db, Bus: b}
		h.New = func(id, def string) (*agent.Agent, *session.Config, error) {
			hub := toolhub.New(toolhub.Options{Store: agent.ToolStore{DB: db}})
			return &agent.Agent{Store: db, SessionID: id, Model: fakeModel{}, Hub: hub},
				&session.Config{Model: "m", Policy: session.DefaultPolicy()}, nil
		}
		return db, b, h
	}
	db1, b1, h1 := open()
	_, _, h2 := open()
	app, err := New(Options{Host: h1, Bus: b1, DB: db1})
	if err != nil {
		t.Fatal(err)
	}
	hd, err := h2.Create(ctx, "x", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	c := app.NewConn(ctx, LocalPrincipal)
	defer c.Close()
	if err := c.Subscribe(ctx, wire.SubscribeArgs{Session: "x"}); err != nil {
		t.Fatal(err)
	}
	waitKind(t, c, wire.EvSynced)
	if err := hd.Do(host.Command{Kind: host.Send, ItemID: "i1", Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	got := waitKind(t, c, wire.EvEntryAppended)
	if e := got[len(got)-1]; e.Cursor == 0 || e.Session != "x" {
		t.Fatalf("应补到另一进程写的条目：%+v", e)
	}
}

// 背压（10.2.3）：先合并 delta、丢 progress / notice；仍积压就断开订阅，lagged 带最后送达的持久游标。
func TestConnBackpressure(t *testing.T) {
	q := []wire.Envelope{
		{Session: "s", Kind: wire.EvMessageDelta, Data: wire.MessageDelta{StreamID: "a", Text: "x"}},
		{Session: "s", Kind: wire.EvNotice, Data: wire.Notice{Text: "n"}},
		{Session: "s", Kind: wire.EvEntryAppended, Cursor: 7, Data: wire.EntryAppended{}},
		{Session: "s", Kind: wire.EvMessageDelta, Data: wire.MessageDelta{StreamID: "a", Text: "y"}},
		{Session: "s", Kind: wire.EvToolProgress, Data: wire.ToolProgress{CallID: "c"}},
	}
	out := compact(q)
	if len(out) != 2 || out[0].Data.(wire.MessageDelta).Text != "xy" || out[1].Cursor != 7 {
		t.Fatalf("合并与丢弃不对：%+v", out)
	}

	oldS, oldH := SoftQueue, HardQueue
	SoftQueue, HardQueue = 4, 8
	defer func() { SoftQueue, HardQueue = oldS, oldH }()
	app := newApp(t)
	c := app.NewConn(context.Background(), LocalPrincipal)
	defer c.Close()
	sctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.mu.Lock()
	c.subs["s"] = &subscription{cancel: cancel}
	c.delivered["s"] = 3
	c.mu.Unlock()
	for i := 0; i < 20; i++ { // 持久事件不合并：一直积压到超过上限
		c.push(sctx, []wire.Envelope{{Session: "s", Kind: wire.EvEntryAppended, Cursor: int64(10 + i), Data: wire.EntryAppended{}}})
	}
	if sctx.Err() == nil {
		t.Fatal("积压超过上限应断开订阅")
	}
	evs, err := c.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	last := evs[len(evs)-1]
	if last.Kind != wire.EvLagged || last.Data.(wire.CursorRef).Cursor != 3 {
		t.Fatalf("应以 lagged{cursor: 3} 结束：%v", kindsOf(evs))
	}
	for _, e := range evs[:len(evs)-1] {
		if e.Session == "s" {
			t.Fatalf("断开后队列里不应留这个会话的事件：%v", kindsOf(evs))
		}
	}
}

// 订阅不存在的会话返回 not_found；游标早于保留期返回 cursor_expired。
func TestConnSubscribeErrors(t *testing.T) {
	app := newApp(t)
	c := app.NewConn(context.Background(), LocalPrincipal)
	defer c.Close()
	_, err := c.Call(context.Background(), "session.subscribe", json.RawMessage(`{"session":"none","after":0}`))
	if e := AsError(err); e == nil || e.Code != wire.CodeNotFound {
		t.Fatalf("不存在的会话应 not_found：%v", err)
	}
}
