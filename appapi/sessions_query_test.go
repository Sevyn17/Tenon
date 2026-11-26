package appapi

// 客户端改进 M7（协议T10、T13）：sessions.changed 带齐列表行字段、hello 带 workspace；
// sessions.list {query, parent, archived}、session.update {archived}、能力 sessions.query / sessions.changed.full。

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
)

// waitAll 一直取事件，直到每个条件都被某条事件满足（事件先后不定时用它，不会像 waitKind 那样吃掉要等的另一条）。
func waitAll(t *testing.T, c *Conn, conds ...func(wire.Envelope) bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	left := slices.Clone(conds)
	for len(left) > 0 {
		evs, err := c.Next(ctx)
		if err != nil {
			t.Fatalf("等事件：%v（还差 %d 个条件）", err, len(left))
		}
		for _, e := range evs {
			left = slices.DeleteFunc(left, func(f func(wire.Envelope) bool) bool { return f(e) })
		}
	}
}

// changed 条件：一条满足 ok 的 sessions.changed。
func changed(ok func(wire.SessionsChanged) bool) func(wire.Envelope) bool {
	return func(e wire.Envelope) bool {
		d, is := e.Data.(wire.SessionsChanged)
		return is && e.Kind == wire.EvSessionsChanged && ok(d)
	}
}

func listIDs(t *testing.T, app *App, a wire.ListArgs) []string {
	t.Helper()
	r, err := app.SessionsList(context.Background(), a)
	if err != nil {
		t.Fatalf("sessions.list %+v：%v", a, err)
	}
	out := []string{}
	for _, x := range r.Items {
		out = append(out, x.ID)
	}
	slices.Sort(out)
	return out
}

func listCode(t *testing.T, app *App, a wire.ListArgs) string {
	t.Helper()
	_, err := app.SessionsList(context.Background(), a)
	var ae *Error
	if !errors.As(err, &ae) {
		t.Fatalf("sessions.list %+v 应返回错误，得到 %v", a, err)
	}
	return ae.Code
}

// plainStore 只暴露 store.Store（隐藏可选接口）。
type plainStore struct{ store.Store }

func storeSession(t *testing.T, app *App, m store.SessionMeta) {
	t.Helper()
	hdr := session.New(&session.Config{Model: "m", Policy: session.DefaultPolicy()}).Snapshot().Header
	if err := app.o.DB.CreateSession(context.Background(), m, hdr, nil); err != nil {
		t.Fatal(err)
	}
}

// 协议T10：回合结束后的 sessions.changed 带 preview、last_end、origin、workspace；子会话带 parent；
// hello 带 workspace 与两项能力；列表行带 preview 与 last_end。
func TestSessionsChangedFullFields(t *testing.T) {
	ctx := context.Background()
	app := newApp(t)
	app.o.Host.Workspace = "wa"
	h, err := app.Hello(ctx, wire.HelloArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if h.Workspace != "wa" || !slices.Contains(h.Capabilities, "sessions.query") || !slices.Contains(h.Capabilities, "sessions.changed.full") {
		t.Fatalf("hello：workspace=%q capabilities=%v", h.Workspace, h.Capabilities)
	}
	if _, e := call2(t, app, "session.create", `{"session":"a"}`); e != nil {
		t.Fatal(e)
	}
	c := subscribeLive(t, app, "a")
	if _, e := call2(t, app, "session.send", `{"session":"a","text":"你好","client_id":"c1"}`); e != nil {
		t.Fatal(e)
	}
	var got wire.SessionsChanged
	waitAll(t, c, changed(func(d wire.SessionsChanged) bool {
		if d.ID == "a" && d.RunState == wire.RunIdle && d.Preview == "re:你好" {
			got = d
			return true
		}
		return false
	}))
	if got.LastEnd == nil || got.LastEnd.Kind != "done" || got.LastEnd.At == 0 || got.Origin != "new" || got.Workspace != "wa" ||
		got.Parent != "" || got.Archived {
		t.Fatalf("sessions.changed 应带齐字段：%+v（last_end %+v）", got, got.LastEnd)
	}
	r, err := app.SessionsList(ctx, wire.ListArgs{})
	if err != nil || len(r.Items) != 1 || r.Items[0].Preview != "re:你好" || r.Items[0].LastEnd == nil || r.Items[0].LastEnd.Kind != "done" {
		t.Fatalf("列表行：%+v %v", r, err)
	}

	// 子会话改名：sessions.changed 带 parent、origin，工作区跟随父会话；改名不改 updated_at。
	storeSession(t, app, store.SessionMeta{ID: "a/sub", ParentID: "a", Origin: "subagent"})
	if _, e := call2(t, app, "session.update", `{"session":"a/sub","title":"子"}`); e != nil {
		t.Fatal(e)
	}
	waitAll(t, c, changed(func(d wire.SessionsChanged) bool {
		return d.ID == "a/sub" && d.Title == "子" && d.Parent == "a" && d.Origin == "subagent" && d.Workspace == "wa" && d.UpdatedAt == 0
	}))
}

// 协议T13：归档后缺省列表没有、archived:only 有；归档取消置顶；已归档的不能直接置顶；发消息自动恢复；
// session.updated 与 sessions.changed 带 archived。
func TestSessionArchive(t *testing.T) {
	app := newApp(t)
	for _, id := range []string{"a", "b", "c"} {
		if _, e := call2(t, app, "session.create", `{"session":"`+id+`"}`); e != nil {
			t.Fatal(e)
		}
	}
	c := subscribeLive(t, app, "a")
	if _, e := call2(t, app, "session.update", `{"session":"a","pinned":true}`); e != nil {
		t.Fatal(e)
	}
	out, e := call2(t, app, "session.update", `{"session":"a","archived":true}`)
	if e != nil {
		t.Fatal(e)
	}
	if s := out.(wire.SessionSnapshot); !s.Archived || s.Pinned {
		t.Fatalf("归档后快照应 archived 且取消置顶：%+v", s)
	}
	waitAll(t, c,
		func(e wire.Envelope) bool {
			d, ok := e.Data.(wire.SessionUpdated)
			return ok && d.Archived && !d.Pinned
		},
		changed(func(d wire.SessionsChanged) bool { return d.ID == "a" && d.Archived && !d.Pinned && d.UpdatedAt == 0 }))

	if got := listIDs(t, app, wire.ListArgs{}); !slices.Equal(got, []string{"b", "c"}) {
		t.Fatalf("缺省列表应排除已归档：%v", got)
	}
	if got := listIDs(t, app, wire.ListArgs{Archived: wire.ArchivedOnly}); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("archived:only：%v", got)
	}
	r, err := app.SessionsList(context.Background(), wire.ListArgs{Archived: wire.ArchivedInclude})
	if err != nil || len(r.Items) != 3 {
		t.Fatalf("archived:include：%+v %v", r, err)
	}
	for _, x := range r.Items {
		if x.Archived != (x.ID == "a") {
			t.Fatalf("列表行的 archived：%+v", r.Items)
		}
	}
	if got := listIDs(t, app, wire.ListArgs{Pinned: true}); len(got) != 0 {
		t.Fatalf("置顶列表：%v", got)
	}
	_, e = call2(t, app, "sessions.list", `{"archived":"yes"}`)
	wantCode(t, e, wire.CodeInvalid, "")

	_, e = call2(t, app, "session.update", `{"session":"a","pinned":true}`)
	wantCode(t, e, wire.CodeConflict, "archived")
	_, e = call2(t, app, "session.update", `{"session":"a","archived":true,"pinned":true}`)
	wantCode(t, e, wire.CodeInvalid, "")
	out, e = call2(t, app, "session.update", `{"session":"a","archived":false,"pinned":true}`)
	if e != nil {
		t.Fatal(e)
	}
	if s := out.(wire.SessionSnapshot); s.Archived || !s.Pinned {
		t.Fatalf("同一请求恢复并置顶：%+v", s)
	}
	if got := listIDs(t, app, wire.ListArgs{Pinned: true}); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("恢复后置顶列表：%v", got)
	}

	// 往已归档的会话发消息：自动恢复（send 返回时已经恢复）。
	if _, e := call2(t, app, "session.update", `{"session":"a","archived":true}`); e != nil {
		t.Fatal(e)
	}
	if _, e := call2(t, app, "session.send", `{"session":"a","text":"hi","client_id":"x1"}`); e != nil {
		t.Fatal(e)
	}
	if s := freshSnapshot(t, app, "a"); s.Archived {
		t.Fatalf("发消息后应自动恢复：%+v", s)
	}
	waitAll(t, c,
		func(e wire.Envelope) bool { return e.Kind == wire.EvTurnEnded },
		changed(func(d wire.SessionsChanged) bool { return d.ID == "a" && !d.Archived && d.UpdatedAt == 0 }))
	if got := listIDs(t, app, wire.ListArgs{}); !slices.Contains(got, "a") {
		t.Fatalf("恢复后缺省列表应有 a：%v", got)
	}
}

// 协议T13：query 走到存储（标题、用户消息正文），工作区照常生效；parent 只列直接子会话、不看工作区；不合法的组合返回 invalid。
func TestSessionsListQueryAndParent(t *testing.T) {
	app := newApp(t)
	app.o.Host.Workspace = "wa"
	for _, id := range []string{"a", "b"} {
		if _, e := call2(t, app, "session.create", `{"session":"`+id+`"}`); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := call2(t, app, "session.update", `{"session":"a","title":"Alpha 计划"}`); e != nil {
		t.Fatal(e)
	}
	c := subscribeLive(t, app, "b")
	sendTurnEnded(t, app, c, "b", "请找到我")
	storeSession(t, app, store.SessionMeta{ID: "w2", Title: "alpha 别处", Workspace: "wb"})
	storeSession(t, app, store.SessionMeta{ID: "w2/sub", ParentID: "w2", Origin: "subagent"})

	if got := listIDs(t, app, wire.ListArgs{Query: "ALPHA"}); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("query 按标题、只在当前工作区：%v", got)
	}
	if got := listIDs(t, app, wire.ListArgs{Query: "alpha", AllWorkspaces: true}); !slices.Equal(got, []string{"a", "w2"}) {
		t.Fatalf("query + all_workspaces：%v", got)
	}
	if got := listIDs(t, app, wire.ListArgs{Query: "找到"}); !slices.Equal(got, []string{"b"}) {
		t.Fatalf("query 按用户消息正文：%v", got)
	}
	if got := listIDs(t, app, wire.ListArgs{Query: "re:"}); len(got) != 0 {
		t.Fatalf("query 不搜助手回复：%v", got)
	}
	if got := listIDs(t, app, wire.ListArgs{Parent: "w2"}); !slices.Equal(got, []string{"w2/sub"}) {
		t.Fatalf("parent 不看工作区与 all：%v", got)
	}
	if got := listIDs(t, app, wire.ListArgs{Parent: "w2", Query: "无"}); len(got) != 0 {
		t.Fatalf("parent + query：%v", got)
	}
	if got := listIDs(t, app, wire.ListArgs{}); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("缺省列表不变：%v", got)
	}
	for _, a := range []wire.ListArgs{{Pinned: true, Query: "a"}, {Pinned: true, Parent: "w2"},
		{Query: strings.Repeat("x", MaxQuery+1)}} {
		if code := listCode(t, app, a); code != wire.CodeInvalid {
			t.Fatalf("%+v 应为 invalid，得到 %s", a, code)
		}
	}
}

// metaOnlyStore 有 SessionMetaStore、没有 SessionQueryStore（第三方存储的样子）。
type metaOnlyStore struct {
	store.Store
	store.SessionMetaStore
}

// 存储不支持查询：query / parent / archived 与归档返回 unsupported（不静默退回全量），不报 sessions.query；缺省列表照常。
func TestSessionsQueryUnsupported(t *testing.T) {
	app := newApp(t)
	if _, e := call2(t, app, "session.create", `{"session":"a"}`); e != nil {
		t.Fatal(e)
	}
	db := app.o.DB
	for name, st := range map[string]store.Store{"plain": plainStore{db}, "metaOnly": metaOnlyStore{db, db.(store.SessionMetaStore)}} {
		app.o.DB = st
		caps := app.capabilities()
		if slices.Contains(caps, "sessions.query") || !slices.Contains(caps, "sessions.changed.full") {
			t.Fatalf("%s：capabilities %v", name, caps)
		}
		for _, a := range []wire.ListArgs{{Query: "x"}, {Parent: "a"}, {Archived: wire.ArchivedOnly}, {Archived: wire.ArchivedInclude}} {
			if code := listCode(t, app, a); code != wire.CodeUnsupported {
				t.Fatalf("%s：%+v 应为 unsupported，得到 %s", name, a, code)
			}
		}
		if got := listIDs(t, app, wire.ListArgs{}); !slices.Equal(got, []string{"a"}) {
			t.Fatalf("%s：缺省列表 %v", name, got)
		}
		_, e := call2(t, app, "session.update", `{"session":"a","archived":true}`)
		wantCode(t, e, wire.CodeUnsupported, "")
	}
}
