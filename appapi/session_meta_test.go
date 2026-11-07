package appapi

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"ybase/appapi/wire"
)

// session.update：改标题、置顶；session.updated 持久事件与 sessions.changed；不改列表顺序。
func TestV2UpdateTitlePin(t *testing.T) {
	app := newApp(t)
	for _, id := range []string{"a", "b"} {
		if _, e := call2(t, app, "session.create", `{"session":"`+id+`"}`); e != nil {
			t.Fatal(e)
		}
	}
	c := app.NewConn(context.Background(), LocalPrincipal)
	defer c.Close()
	if err := c.Subscribe(context.Background(), wire.SubscribeArgs{Session: "a"}); err != nil {
		t.Fatal(err)
	}
	waitKind(t, c, wire.EvSynced)

	out, e := call2(t, app, "session.update", `{"session":"a","title":"  我的会话  ","pinned":true}`)
	if e != nil {
		t.Fatal(e)
	}
	snap := out.(wire.SessionSnapshot)
	if snap.Title != "我的会话" || !snap.Pinned {
		t.Fatalf("快照：%+v", snap)
	}
	// session.updated（订阅）与 sessions.changed（连接级）先后不定：两个都等到为止。
	var up *wire.Envelope
	var ch *wire.SessionsChanged
	for up == nil || ch == nil {
		evs := waitKind(t, c, map[bool]string{true: wire.EvSessionUpdated, false: wire.EvSessionsChanged}[up == nil])
		for i := range evs {
			switch d := evs[i].Data.(type) {
			case wire.SessionUpdated:
				up = &evs[i]
			case wire.SessionsChanged:
				ch = &d
			}
		}
	}
	if d := up.Data.(wire.SessionUpdated); d.Title != "我的会话" || !d.Pinned || up.Cursor == 0 || up.Cursor != snap.Cursor {
		t.Fatalf("session.updated：%+v（快照 cursor %d）", up, snap.Cursor)
	}
	if ch.ID != "a" || ch.Title != "我的会话" || !ch.Pinned || ch.UpdatedAt != 0 {
		t.Fatalf("sessions.changed：%+v", ch)
	}

	list, _ := call2(t, app, "sessions.list", `{}`)
	items := list.(wire.ListResult).Items
	if len(items) != 2 || items[0].ID != "b" || !items[1].Pinned {
		t.Fatalf("改标题不应改顺序，行带 pinned：%+v", items)
	}
	pinned, _ := call2(t, app, "sessions.list", `{"pinned":true}`)
	if p := pinned.(wire.ListResult).Items; len(p) != 1 || p[0].ID != "a" {
		t.Fatalf("置顶列表：%+v", p)
	}

	if _, e := call2(t, app, "session.update", `{"session":"a","pinned":false}`); e != nil {
		t.Fatal(e)
	}
	if p, _ := call2(t, app, "sessions.list", `{"pinned":true}`); len(p.(wire.ListResult).Items) != 0 {
		t.Fatalf("取消置顶后：%+v", p)
	}
	if s, _ := call2(t, app, "session.get", `{"session":"a"}`); s.(wire.SessionSnapshot).Title != "我的会话" {
		t.Fatal("只改 pinned 不应动标题")
	}

	_, e = call2(t, app, "session.update", `{"session":"a","title":"x\ny"}`)
	wantCode(t, e, wire.CodeInvalid, "")
	_, e = call2(t, app, "session.update", `{"session":"none","title":"x"}`)
	wantCode(t, e, wire.CodeNotFound, "")
}

// session.delete：删除顶层与子会话；订阅方收到 session.closed {deleted}，列表收到 sessions.changed {deleted}；重删 not_found。
func TestV2Delete(t *testing.T) {
	app := newApp(t)
	if _, e := call2(t, app, "session.create", `{"session":"d"}`); e != nil {
		t.Fatal(e)
	}
	sendWait(t, app, "d", "hi", "c1")
	if _, err := app.Host().Spawn(context.Background(), "d", "d/sub", "", app.Host().New, "子任务"); err != nil {
		t.Fatal(err)
	}
	if _, e := call2(t, app, "session.fork", `{"session":"d","to":"d-fork"}`); e != nil {
		t.Fatal(e)
	}
	c := app.NewConn(context.Background(), LocalPrincipal)
	defer c.Close()
	if err := c.Subscribe(context.Background(), wire.SubscribeArgs{Session: "d"}); err != nil {
		t.Fatal(err)
	}
	waitKind(t, c, wire.EvSynced)

	if _, e := call2(t, app, "session.delete", `{"session":"d/sub"}`); e == nil || e.Code != wire.CodeInvalid {
		t.Fatalf("单删子会话应 invalid：%+v", e)
	}
	if _, e := call2(t, app, "session.delete", `{"session":"d"}`); e != nil {
		t.Fatal(e)
	}
	got := waitKind(t, c, wire.EvSessionClosed)
	if d := got[len(got)-1].Data.(wire.SessionClosed); d.Reason != "deleted" {
		t.Fatalf("session.closed：%+v", d)
	}
	deleted := map[string]bool{}
	note := func(evs []wire.Envelope) {
		for _, e := range evs {
			if d, ok := e.Data.(wire.SessionsChanged); ok && d.Deleted {
				deleted[d.ID] = true
			}
		}
	}
	note(got) // 连接级事件可能先于 session.closed 到达
	for len(deleted) < 2 {
		note(waitKind(t, c, wire.EvSessionsChanged))
	}
	if !deleted["d"] || !deleted["d/sub"] {
		t.Fatalf("sessions.changed deleted：%v", deleted)
	}
	for _, id := range []string{"d", "d/sub"} {
		_, e := call2(t, app, "session.get", `{"session":"`+id+`"}`)
		wantCode(t, e, wire.CodeNotFound, "")
		if _, ok := app.Host().Get(id); ok {
			t.Fatalf("%s 应已关闭", id)
		}
	}
	if _, e := call2(t, app, "session.get", `{"session":"d-fork"}`); e != nil {
		t.Fatalf("分叉出去的会话不应删：%v", e)
	}
	_, e := call2(t, app, "session.delete", `{"session":"d"}`)
	wantCode(t, e, wire.CodeNotFound, "")
}

// workspace.files：未装配时 unsupported；装配后按 limit 返回，能力清单带 files。
func TestV2WorkspaceFiles(t *testing.T) {
	app := newApp(t)
	_, e := call2(t, app, "workspace.files", `{}`)
	wantCode(t, e, wire.CodeUnsupported, "")
	var gotQ string
	var gotLimit int
	app.o.Files = func(_ context.Context, q string, limit int) ([]wire.FileInfo, bool, error) {
		gotQ, gotLimit = q, limit
		return []wire.FileInfo{{Path: "a.go", Size: 1, MTime: 2}}, true, nil
	}
	out, e := call2(t, app, "workspace.files", `{"query":"a","limit":9999}`)
	if e != nil {
		t.Fatal(e)
	}
	r := out.(wire.FilesResult)
	if len(r.Files) != 1 || r.Files[0].Path != "a.go" || !r.Truncated || gotQ != "a" || gotLimit != 50 {
		t.Fatalf("结果 %+v q=%q limit=%d", r, gotQ, gotLimit)
	}
	h, _ := call2(t, app, "hello", `{"client":"t"}`)
	found := false
	for _, c := range h.(wire.HelloResult).Capabilities {
		found = found || c == "files"
	}
	if !found {
		t.Fatal("capabilities 应带 files")
	}
}

type fakeModels struct{}

func (fakeModels) List() wire.ModelsListResult {
	return wire.ModelsListResult{Default: "m", Models: []wire.ModelInfo{{ID: "m", Provider: "p", Available: true}, {ID: "p/big", Provider: "p", Available: true}}}
}

func (fakeModels) Check(id string) error {
	if id == "m" || id == "p/big" {
		return nil
	}
	return errors.New("目录里没有")
}

// session.settings {model}：校验、落库、settings_changed 带 model、快照带 model。
func TestV2SwitchModel(t *testing.T) {
	app := newApp(t)
	_, e := call2(t, app, "models.list", `{}`)
	wantCode(t, e, wire.CodeUnsupported, "")
	app.o.Models = fakeModels{}
	if _, e := call2(t, app, "session.create", `{"session":"m1"}`); e != nil {
		t.Fatal(e)
	}
	out, e := call2(t, app, "models.list", `{}`)
	if e != nil || len(out.(wire.ModelsListResult).Models) != 2 {
		t.Fatalf("models.list：%v %v", out, e)
	}
	c := app.NewConn(context.Background(), LocalPrincipal)
	defer c.Close()
	if err := c.Subscribe(context.Background(), wire.SubscribeArgs{Session: "m1"}); err != nil {
		t.Fatal(err)
	}
	waitKind(t, c, wire.EvSynced)
	_, e = call2(t, app, "session.settings", `{"session":"m1","model":"nope"}`)
	wantCode(t, e, wire.CodeInvalid, "")
	r, e := call2(t, app, "session.settings", `{"session":"m1","model":"p/big"}`)
	if e != nil || r.(wire.SettingsResult).Settings.Model != "p/big" || !r.(wire.SettingsResult).Committed {
		t.Fatalf("settings：%+v %v", r, e)
	}
	got := waitKind(t, c, wire.EvSettingsChanged)
	if d := got[len(got)-1].Data.(wire.SettingsChanged); d.Settings.Model != "p/big" {
		t.Fatalf("settings_changed：%+v", d)
	}
	snap, _ := call2(t, app, "session.get", `{"session":"m1"}`)
	if snap.(wire.SessionSnapshot).Settings.Model != "p/big" {
		t.Fatalf("快照：%+v", snap.(wire.SessionSnapshot).Settings)
	}
	sendWait(t, app, "m1", "hi", "c1") // 换了模型后照常能跑回合（fakeModel 不看别名）
}

// feedback.set / list：打分、改分、撤销；按连接身份记 by；不改列表顺序；随会话删除。
func TestV2Feedback(t *testing.T) {
	app := newApp(t)
	if _, e := call2(t, app, "session.create", `{"session":"f"}`); e != nil {
		t.Fatal(e)
	}
	c := app.NewConn(context.Background(), wire.Principal{ID: "alice", Role: wire.RoleOperator})
	defer c.Close()
	callC := func(cmd, args string) (any, error) { return c.Call(context.Background(), cmd, json.RawMessage(args)) }
	if _, err := callC("feedback.set", `{"session":"f","entry":"e1","vote":"up"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := callC("feedback.set", `{"session":"f","entry":"e2","vote":"down","note":"答非所问"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := callC("feedback.set", `{"session":"f","entry":"e1","vote":"down"}`); err != nil {
		t.Fatal(err)
	}
	out, _ := call2(t, app, "feedback.list", `{"session":"f"}`)
	items := out.(wire.FeedbackListResult).Items
	byEntry := map[string]wire.Feedback{}
	for _, f := range items {
		byEntry[f.Entry] = f
	}
	if len(items) != 2 || byEntry["e1"].Vote != "down" || byEntry["e2"].Note != "答非所问" || byEntry["e2"].By != "alice" || items[0].At > items[1].At {
		t.Fatalf("列表：%+v", items)
	}
	if _, err := callC("feedback.set", `{"session":"f","entry":"e1","vote":""}`); err != nil {
		t.Fatal(err)
	}
	out, _ = call2(t, app, "feedback.list", `{"session":"f"}`)
	if len(out.(wire.FeedbackListResult).Items) != 1 {
		t.Fatalf("撤销后：%+v", out)
	}
	if _, err := callC("feedback.set", `{"session":"f","entry":"e1","vote":"meh"}`); AsError(err).Code != wire.CodeInvalid {
		t.Fatalf("非法 vote：%v", err)
	}
	if _, err := callC("feedback.set", `{"session":"none","entry":"e1","vote":"up"}`); AsError(err).Code != wire.CodeNotFound {
		t.Fatalf("会话不存在：%v", err)
	}
	v := app.NewConn(context.Background(), wire.Principal{ID: "v", Role: wire.RoleViewer})
	defer v.Close()
	if _, err := v.Call(context.Background(), "feedback.set", json.RawMessage(`{"session":"f","entry":"e1","vote":"up"}`)); AsError(err).Code != wire.CodeForbidden {
		t.Fatalf("viewer 不能打分：%v", err)
	}
	if _, err := v.Call(context.Background(), "feedback.list", json.RawMessage(`{"session":"f"}`)); err != nil {
		t.Fatalf("viewer 能看：%v", err)
	}
}
