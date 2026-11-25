package sessions

// C2（Mortise docs/76）：session_start 放开 independent 与 notify / none；回投只在创建者那封信触发的回合后发生，
// 以不可信 notice 进创建者；并发上限先登记后建会话、失败回滚。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/runtime/callctx"
	"github.com/Sevyn17/tenon/runtime/host"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
	"github.com/Sevyn17/tenon/runtime/store/memstore"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

func c2Call(t *testing.T, tool *toolhub.Tool, sid, cid, args string) toolhub.Output {
	t.Helper()
	kc := callctx.New(callctx.Options{SessionID: sid})
	kc.Bind(callctx.Session{ID: sid, ChainDepth: 1}, nil, nil)
	out, err := tool.Call(context.Background(), &toolhub.Cx{SessionID: sid, CallID: cid, Tool: tool, Ctx: kc}, json.RawMessage(args))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// notices 会话 id 已提交历史里的 status 条目（回投 notice 落在这里）。
func notices(t *testing.T, db *memstore.Store, id string) []*session.Entry {
	t.Helper()
	l, err := db.Load(context.Background(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	var out []*session.Entry
	for _, e := range l.Snapshot.Entries {
		if e.Item.Injection != nil && strings.Contains(e.Item.Injection.Text, "<status-update>") {
			out = append(out, e)
		}
	}
	return out
}

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if f() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等不到：%s", what)
}

func TestIndependentNotifyOnlyForCreatorTurn(t *testing.T) {
	ctx := context.Background()
	h, db := chainHost(t)
	if _, err := h.Create(ctx, "p", "", nil); err != nil {
		t.Fatal(err)
	}
	tool := ToolWithOptions(h, h.New, nil, Params{}, nil, 4)
	out := c2Call(t, tool, "p", "call-000000c1", `{"task":"查一下","relation":"independent"}`)
	if out.Status != session.ToolOK || !strings.Contains(contentText(out.Content), "已派出独立会话") {
		t.Fatalf("独立会话应派出：%+v", out)
	}
	ls, _ := db.QuerySessions(ctx, store.SessionQuery{CreatorSession: "p"})
	if len(ls) != 1 {
		t.Fatalf("应有一个由 p 建的会话：%+v", ls)
	}
	nid := ls[0].ID
	l, err := host.LoadHead(ctx, db, nid)
	if err != nil {
		t.Fatal(err)
	}
	if l.Meta.ParentID != "" || l.Meta.CreatorSession != "p" || l.Meta.Depth() != 2 || l.Meta.Root() != "p" || l.Meta.ReplyMode != host.ReplyNotify {
		t.Fatalf("独立会话的链字段：%+v", l.Meta)
	}
	// 回投：创建者收到一条不可信 notice，带对方回复。
	waitFor(t, "回投 notice", func() bool { return len(notices(t, db, "p")) == 1 })
	n := notices(t, db, "p")[0]
	if n.Meta[session.MetaTrust] != "untrusted" || !strings.Contains(n.Item.Injection.Text, "查一下") || !strings.Contains(n.Item.Injection.Text, "session:"+nid) {
		t.Fatalf("回投应是不可信 notice 并带对方回复：%+v %s", n.Meta, n.Item.Injection.Text)
	}
	// 用户直接在独立会话里发消息：这一轮不回投。
	hd, err := h.Open(ctx, nid)
	if err != nil {
		t.Fatal(err)
	}
	if err := hd.Do(host.Command{Kind: host.Send, Text: "再聊一句"}); err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if got := len(notices(t, db, "p")); got != 1 {
		t.Fatalf("用户触发的回合不应回投：%d 条 notice", got)
	}
}

func TestChildNoneDoesNotReply(t *testing.T) {
	ctx := context.Background()
	h, db := chainHost(t)
	if _, err := h.Create(ctx, "p", "", nil); err != nil {
		t.Fatal(err)
	}
	tool := ToolWithOptions(h, h.New, nil, Params{}, nil, 4)
	out := c2Call(t, tool, "p", "call-000000c2", `{"task":"静默做","reply":"none"}`)
	if out.Status != session.ToolOK {
		t.Fatalf("child+none 应派出：%+v", out)
	}
	id := "p/sub-000000c2"
	l, err := host.LoadHead(ctx, db, id)
	if err != nil {
		t.Fatal(err)
	}
	if l.Meta.ParentID != "p" || l.Meta.ReplyMode != host.ReplyNone {
		t.Fatalf("异步子会话字段：%+v", l.Meta)
	}
	hd, _ := h.Get(id)
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if got := len(notices(t, db, "p")); got != 0 {
		t.Fatalf("reply=none 不应回投：%d", got)
	}
}

func TestIndependentWaitRejected(t *testing.T) {
	h, _ := chainHost(t)
	tool := ToolWithOptions(h, h.New, nil, Params{}, nil, 4)
	out := c2Call(t, tool, "p", "call-000000c3", `{"task":"x","relation":"independent","reply":"wait"}`)
	if out.Status == session.ToolOK || !strings.Contains(contentText(out.Content), "不能配 reply=wait") {
		t.Fatalf("independent+wait 应拒绝：%+v", out)
	}
}

func TestConcurrencyLimitAndRollback(t *testing.T) {
	var c concurrency
	r1, err := c.acquire("p", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.acquire("p", 1); err == nil || !strings.Contains(err.Error(), "并发上限（1）") || !strings.Contains(err.Error(), "session_list") {
		t.Fatalf("超上限应拒绝并提示 session_list：%v", err)
	}
	if _, err := c.acquire("q", 1); err != nil {
		t.Fatalf("按派出方分片：%v", err)
	}
	r1()
	r1() // 幂等
	if c.inUse("p") != 0 {
		t.Fatalf("回滚后应为 0：%d", c.inUse("p"))
	}
	if clampConcurrent(0) != 4 || clampConcurrent(99) != MaxConcurrentCap || clampConcurrent(2) != 2 {
		t.Fatal("clamp")
	}
}

// 建会话失败（派出方不存在）时回滚名额：上限 1 下连续两次失败的调用都拿得到名额。
func TestConcurrencyRollbackOnStartFailure(t *testing.T) {
	h, _ := chainHost(t)
	tool := ToolWithOptions(h, h.New, nil, Params{}, nil, 1)
	for _, cid := range []string{"call-000000d1", "call-000000d2"} {
		out := c2Call(t, tool, "ghost", cid, `{"task":"x","relation":"independent"}`)
		if out.Status == session.ToolOK || strings.Contains(contentText(out.Content), "并发上限") {
			t.Fatalf("应按建会话失败报错、名额已回滚：%+v", out)
		}
	}
}
