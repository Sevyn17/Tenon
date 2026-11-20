package appapi

import (
	"context"
	"slices"
	"testing"

	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/runtime/host"
)

// noQueueStore 隐藏 Unqueue：模拟没实现 store.QueueStore 的存储。
type noQueueStore struct{ host.Store }

// 能力 send.queue、快照 queued、session.unqueue 与 queue.changed（客户端改进 M12 协议T20）。
func TestQueueWire(t *testing.T) {
	app := newApp(t)
	if !slices.Contains(app.capabilities(), CapSendQueue) {
		t.Fatalf("应报 %s：%v", CapSendQueue, app.capabilities())
	}
	if _, e := call2(t, app, "session.create", `{"session":"s"}`); e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	c := app.NewConn(ctx, LocalPrincipal)
	defer c.Close()
	if err := c.Subscribe(ctx, wire.SubscribeArgs{Session: "s"}); err != nil {
		t.Fatal(err)
	}
	waitKind(t, c, wire.EvSynced)
	// 回合停在工具确认上（askGate），期间排队的消息一定不会被处理：测试不依赖调度时机。
	// （原先直接往空闲会话的信箱放排队项，actor 一被叫醒就会合法地认领它，偶发失败。）
	if _, e := call2(t, app, "session.send", `{"session":"s","text":"call tool","client_id":"c0"}`); e != nil {
		t.Fatal(e)
	}
	waitKind(t, c, wire.EvInteractionOpen)
	r, e := call2(t, app, "session.send", `{"session":"s","text":"稍后","client_id":"q1","queue":true}`)
	if e != nil || !r.(wire.SendResult).Queued {
		t.Fatalf("排队发送：%v %v", r, e)
	}
	evs := waitKind(t, c, wire.EvQueueChanged)
	if qc := evs[len(evs)-1].Data.(wire.QueueChanged); len(qc.Queued) != 1 || qc.Queued[0].ID != "q1" {
		t.Fatalf("queue.changed 应含 q1：%+v", qc)
	}
	r, e = call2(t, app, "session.get", `{"session":"s"}`)
	if e != nil {
		t.Fatal(e)
	}
	snap := r.(wire.SessionSnapshot)
	if len(snap.Queued) != 1 || snap.Queued[0].ID != "q1" || len(snap.Queued[0].Content) != 1 || snap.Queued[0].Content[0].Text != "稍后" || snap.Queued[0].At == 0 {
		t.Fatalf("快照 queued：%+v", snap.Queued)
	}
	r, e = call2(t, app, "session.unqueue", `{"session":"s","id":"q1"}`)
	if e != nil || !r.(wire.UnqueueResult).Removed {
		t.Fatalf("取消：%v %v", r, e)
	}
	evs = waitKind(t, c, wire.EvQueueChanged)
	if qc := evs[len(evs)-1].Data.(wire.QueueChanged); qc.Queued == nil || len(qc.Queued) != 0 {
		t.Fatalf("queue.changed 应为空列表：%+v", qc)
	}
	if _, e := call2(t, app, "session.cancel", `{"session":"s"}`); e != nil {
		t.Fatal(e)
	}
	hd, _ := app.Host().Get("s")
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	if r, _ := call2(t, app, "session.unqueue", `{"session":"s","id":"q1"}`); r.(wire.UnqueueResult).Removed {
		t.Fatal("重复取消应 removed=false")
	}
	if r, _ = call2(t, app, "session.get", `{"session":"s"}`); r.(wire.SessionSnapshot).Queued != nil {
		t.Fatalf("取消后快照不应有 queued：%+v", r.(wire.SessionSnapshot).Queued)
	}
	_, e = call2(t, app, "session.unqueue", `{"session":"s"}`)
	wantCode(t, e, wire.CodeBadRequest, "")
	// 空闲时带 queue 的 send 照常开回合。
	if _, e := call2(t, app, "session.send", `{"session":"s","text":"hi","client_id":"c1","queue":true}`); e != nil {
		t.Fatal(e)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, m := range hd.Agent().CommittedHistory() {
		for _, p := range m.Content {
			texts = append(texts, m.Role+":"+p.Text)
		}
	}
	if slices.Contains(texts, "user:稍后") {
		t.Fatalf("取消的排队消息不应执行：%v", texts)
	}
	if len(texts) == 0 || texts[len(texts)-1] != "assistant:re:hi" {
		t.Fatalf("空闲时带 queue 的 send 应照常跑完：%v", texts)
	}
}

// 存储不支持排队：不报能力，session.unqueue 返回 unsupported，send 的 queue 被忽略（按原行为处理）。
func TestQueueUnsupportedStore(t *testing.T) {
	app := newAppOpts(t, func(o *Options) { o.Host.DB = noQueueStore{o.Host.DB} })
	if slices.Contains(app.capabilities(), CapSendQueue) {
		t.Fatalf("不应报 %s", CapSendQueue)
	}
	if _, e := call2(t, app, "session.create", `{"session":"s"}`); e != nil {
		t.Fatal(e)
	}
	_, e := call2(t, app, "session.unqueue", `{"session":"s","id":"x"}`)
	wantCode(t, e, wire.CodeUnsupported, "")
	if _, e := call2(t, app, "session.send", `{"session":"s","text":"hi","client_id":"c1","queue":true}`); e != nil {
		t.Fatal(e)
	}
	hd, _ := app.Host().Get("s")
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	items, _ := app.o.Host.DB.Inbox(context.Background(), "s")
	if len(items) != 0 {
		t.Fatalf("信箱应已处理：%+v", items)
	}
}
