package appapi

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ybase-kernel/core/agent"
	"ybase-kernel/core/bus"
	"ybase-kernel/core/host"
	"ybase-kernel/core/session"
	"ybase-kernel/core/store/sqlite"
	"ybase-kernel/core/toolhub"
)

// fakeModel：最后一条是 user → 含 "tool" 调 echo，否则回 "re:<文本>"（经 onText 流式）；最后一条是 tool → 回 "done"。
type fakeModel struct{}

func (fakeModel) Stream(_ context.Context, req session.Request, onText func(string)) (session.AssistantItem, session.StopReason, error) {
	last := req.Messages[len(req.Messages)-1]
	txt := ""
	for _, p := range last.Content {
		txt += p.Text
	}
	if last.Role == "tool" {
		return session.AssistantItem{Content: session.Content{session.NewTextPart("done")}}, session.StopReason{V: "stop"}, nil
	}
	if strings.Contains(txt, "tool") {
		return session.AssistantItem{ToolCalls: []session.ToolCall{{Name: "echo", Args: json.RawMessage(`{"s":"x"}`)}}},
			session.StopReason{V: "tool_calls"}, nil
	}
	out := "re:" + txt
	if onText != nil {
		onText(out)
	}
	return session.AssistantItem{Content: session.Content{session.NewTextPart(out)}}, session.StopReason{V: "stop"}, nil
}

// askLayer 危险工具一律要确认（模拟权限插件）。
func askLayer() toolhub.Layer {
	return toolhub.Layer{Name: "permission", Guard: true, Call: func(ctx context.Context, cx *toolhub.Cx, args json.RawMessage, next toolhub.Next) (toolhub.Output, error) {
		if cx.Approved || (cx.Tool != nil && cx.Tool.Risk == toolhub.Read) {
			return next(ctx, cx, args)
		}
		return toolhub.Output{Kind: toolhub.Confirm, Reason: cx.Tool.Spec.Name + " 需要确认"}, nil
	}}
}

func newApp(t *testing.T) *App {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "s.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	b := bus.New(0)
	t.Cleanup(func() { b.Close(); db.Close() })
	var app *App
	h := &host.Host{DB: db, Bus: b}
	h.New = func(id, def string) (*agent.Agent, *session.Config, error) {
		hub := toolhub.New(toolhub.Options{Store: agent.ToolStore{DB: db}})
		echo := &toolhub.Tool{Spec: session.ToolSpec{Name: "echo", Schema: json.RawMessage(`{"type":"object"}`)},
			Risk: toolhub.Dangerous, Call: func(context.Context, *toolhub.Cx, json.RawMessage) (toolhub.Output, error) {
				return toolhub.Text("echoed"), nil
			}}
		if err := hub.Register(echo); err != nil {
			return nil, nil, err
		}
		hub.AddLayer(askLayer())
		a := &agent.Agent{Store: db, SessionID: id, Model: fakeModel{}, Hub: hub, Hooks: app.StreamHooks(id)}
		cfg := &session.Config{Model: "m", Tools: hub.Exposed(hub.SpecsFor(nil, 0)), Policy: session.DefaultPolicy()}
		return a, cfg, nil
	}
	app, err = New(Options{Host: h, Bus: b, DB: db, Model: "m", Workdir: "w"})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

// next 等到指定类型的事件。
func next(t *testing.T, s *bus.Sub, kind string) bus.Envelope {
	t.Helper()
	tm := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-s.C():
			if !ok {
				t.Fatalf("订阅已关闭（等 %s）：%v", kind, s.Err())
			}
			if ev.Kind == kind {
				return ev
			}
		case <-tm:
			t.Fatalf("超时：没等到 %s", kind)
		}
	}
}

func TestCallUnknownAndBadArgs(t *testing.T) {
	app := newApp(t)
	ctx := context.Background()
	_, err := app.Call(ctx, "nope", nil)
	if e := AsError(err); e == nil || e.Code != CodeUnsupported {
		t.Fatalf("未知命令应为 unsupported：%v", err)
	}
	_, err = app.Call(ctx, "session.open", json.RawMessage(`{"session":1}`))
	if e := AsError(err); e == nil || e.Code != CodeBadRequest {
		t.Fatalf("参数类型错应为 bad_request：%v", err)
	}
	_, err = app.Call(ctx, "session.send", json.RawMessage(`{}`))
	if e := AsError(err); e == nil || e.Code != CodeBadRequest {
		t.Fatalf("缺 session 应为 bad_request：%v", err)
	}
	_, err = app.Call(ctx, "hello", json.RawMessage(`{"client":"t","version":99}`))
	if e := AsError(err); e == nil || e.Code != CodeUnsupported {
		t.Fatalf("协议版本不符应为 unsupported：%v", err)
	}
	out, err := app.Call(ctx, "hello", json.RawMessage(`{"client":"t","version":1}`))
	if err != nil || out.(HelloResult).Protocol != Protocol {
		t.Fatalf("hello：%v %v", out, err)
	}
	_, err = app.Call(ctx, "mode.list", nil)
	if e := AsError(err); e == nil || e.Code != CodeUnsupported {
		t.Fatalf("未装权限插件 mode.list 应为 unsupported：%v", err)
	}
	_, err = app.Call(ctx, "session.cancel", json.RawMessage(`{"session":"none"}`))
	if e := AsError(err); e == nil || e.Code != CodeNotFound {
		t.Fatalf("未打开会话 cancel 应为 not_found：%v", err)
	}
	if b, _ := json.Marshal(AsError(err)); !strings.Contains(string(b), `"code":"not_found"`) {
		t.Fatalf("错误 JSON 形态：%s", b)
	}
}

func TestSendFollowEntries(t *testing.T) {
	app := newApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := app.Call(ctx, "session.open", json.RawMessage(`{"session":"s1"}`)); err != nil {
		t.Fatal(err)
	}
	fol, err := app.Follow(ctx, "s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	vol := app.Events(ctx, bus.Filter{Session: "s1", Kinds: []string{EventTextDelta, EventTurnEnd}})
	next(t, fol, bus.KindSynced)
	if _, err := app.Call(ctx, "session.send", json.RawMessage(`{"session":"s1","text":"你好"}`)); err != nil {
		t.Fatal(err)
	}
	e1 := next(t, fol, sqlite.EventEntry)
	e2 := next(t, fol, sqlite.EventEntry)
	if e1.Seq <= 0 || e2.Seq <= e1.Seq {
		t.Fatalf("持久事件 seq 应递增：%d %d", e1.Seq, e2.Seq)
	}
	d := next(t, vol, EventTextDelta)
	if m, _ := d.Data.(map[string]any); m["text"] != "re:你好" {
		t.Fatalf("text_delta：%v", d.Data)
	}
	next(t, vol, EventTurnEnd)

	res, err := app.Entries(ctx, EntriesArgs{Session: "s1"})
	if err != nil || len(res.Entries) != 2 || res.LogLen != 2 {
		t.Fatalf("entries：%+v %v", res, err)
	}
	// 断线重连：after = 第一条的 seq，只回放第二条。
	fol2, err := app.Follow(ctx, "s1", e1.Seq)
	if err != nil {
		t.Fatal(err)
	}
	if ev := next(t, fol2, sqlite.EventEntry); ev.Seq != e2.Seq {
		t.Fatalf("重连回放应从 %d 开始：%d", e2.Seq, ev.Seq)
	}
}

// E 组：界面可以按 corr 回信应答确认（session.reply），并用 trace.explain 看一次任务的路径。
func TestReplyByCorrAndExplain(t *testing.T) {
	app := newApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := app.Events(ctx, bus.Filter{Kinds: []string{EventHumanRequested, EventToolResult, EventTurnEnd}})
	if _, err := app.Open(ctx, SessionArgs{Session: "s1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Send(ctx, SendArgs{Session: "s1", Text: "call tool"}); err != nil {
		t.Fatal(err)
	}
	hr := next(t, sub, EventHumanRequested)
	m, _ := hr.Data.(map[string]any)
	corr, _ := m["corr"].(string)
	if corr == "" {
		t.Fatalf("human_requested 应带 corr：%v", hr.Data)
	}
	if _, err := app.Call(ctx, "session.reply", json.RawMessage(`{"session":"s1","corr":"`+corr+`","approve":true}`)); err != nil {
		t.Fatalf("session.reply: %v", err)
	}
	if r, _ := next(t, sub, EventToolResult).Data.(map[string]any); r["preview"] != "echoed" {
		t.Fatalf("回信批准后工具应执行：%v", r)
	}
	next(t, sub, EventTurnEnd)
	// 同一 corr 再回信：已作废，拒绝。
	if _, err := app.Call(ctx, "session.reply", json.RawMessage(`{"session":"s1","corr":"`+corr+`","approve":true}`)); err == nil {
		t.Fatal("重复回信应被拒")
	}
	if _, err := app.Call(ctx, "trace.explain", json.RawMessage(`{}`)); AsError(err) == nil || AsError(err).Code != CodeBadRequest {
		t.Fatalf("缺 trace 应为 bad_request：%v", err)
	}
	res, err := app.Call(ctx, "trace.explain", json.RawMessage(`{"trace":"none"}`))
	if err != nil || !strings.Contains(res.(TraceResult).Text, "没有记录") {
		t.Fatalf("trace.explain：%v %v", res, err)
	}
}

func TestConfirmFlow(t *testing.T) {
	app := newApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := app.Events(ctx, bus.Filter{Kinds: []string{EventHumanRequested, EventToolCall, EventToolResult, EventTurnEnd}})
	if _, err := app.Open(ctx, SessionArgs{Session: "s1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Send(ctx, SendArgs{Session: "s1", Text: "call tool"}); err != nil {
		t.Fatal(err)
	}
	hr := next(t, sub, EventHumanRequested)
	m, _ := hr.Data.(map[string]any)
	callID, _ := m["call_id"].(string)
	if callID == "" || m["tool"] != "echo" {
		t.Fatalf("human_requested：%v", hr.Data)
	}
	if _, err := app.Confirm(ctx, ConfirmArgs{Session: "s1", CallID: callID, Decision: "maybe"}); AsError(err) == nil || AsError(err).Code != CodeBadRequest {
		t.Fatalf("非法 decision 应为 bad_request：%v", err)
	}
	if _, err := app.Confirm(ctx, ConfirmArgs{Session: "s1", CallID: callID, Decision: DecisionSession}); AsError(err) == nil || AsError(err).Code != CodeUnsupported {
		t.Fatalf("未装权限插件 decision=session 应为 unsupported：%v", err)
	}
	if _, err := app.Confirm(ctx, ConfirmArgs{Session: "s1", CallID: callID, Decision: DecisionOnce}); err != nil {
		t.Fatal(err)
	}
	tr := next(t, sub, EventToolResult)
	if r, _ := tr.Data.(map[string]any); r["call_id"] != callID || r["preview"] != "echoed" || r["error"] != false {
		t.Fatalf("tool_result：%v", tr.Data)
	}
	next(t, sub, EventTurnEnd)
	res, err := app.Entries(ctx, EntriesArgs{Session: "s1", Tail: 1})
	if err != nil || len(res.Entries) != 1 {
		t.Fatalf("entries tail：%+v %v", res, err)
	}
	if txt := res.Entries[0].Entry.Item.Assistant; txt == nil || len(txt.Content) == 0 || txt.Content[0].Text != "done" {
		t.Fatalf("最后一条应为 done：%+v", res.Entries[0].Entry.Item)
	}
}

func TestSessionsList(t *testing.T) {
	app := newApp(t)
	ctx := context.Background()
	for _, id := range []string{"a", "b", "c"} {
		if _, err := app.Open(ctx, SessionArgs{Session: id}); err != nil {
			t.Fatal(err)
		}
		if _, err := app.Send(ctx, SendArgs{Session: id, Text: "hi " + id}); err != nil {
			t.Fatal(err)
		}
		hd, _ := app.Host().Get(id)
		if err := hd.Wait(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond) // updated_at 毫秒精度，拉开顺序
	}
	if _, err := app.Fork(ctx, ForkArgs{Session: "a", To: "a2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Fork(ctx, ForkArgs{Session: "a", To: "a2"}); AsError(err) == nil || AsError(err).Code != CodeConflict {
		t.Fatalf("目标已存在应为 conflict：%v", err)
	}
	out, err := app.Call(ctx, "sessions.list", json.RawMessage(`{"limit":2,"all":true}`))
	if err != nil {
		t.Fatal(err)
	}
	p1 := out.(ListResult)
	if len(p1.Items) != 2 || p1.Next == 0 {
		t.Fatalf("第一页：%+v", p1)
	}
	p2, err := app.SessionsList(ctx, ListArgs{Cursor: p1.Next, Limit: 2, All: true})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, it := range append(p1.Items, p2.Items...) {
		seen[it.ID] = true
	}
	for _, id := range []string{"a", "b", "c", "a2"} {
		if !seen[id] {
			t.Fatalf("分页应列出 %s：%v", id, seen)
		}
	}
	top, err := app.SessionsList(ctx, ListArgs{})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range top.Items {
		if it.ID == "a2" {
			t.Fatalf("缺省只列顶层会话，不应含分叉 a2：%+v", top.Items)
		}
	}
	if len(top.Items) != 3 {
		t.Fatalf("顶层会话应为 3 个：%+v", top.Items)
	}
	if b, _ := json.Marshal(p1); !strings.Contains(string(b), `"items"`) || !strings.Contains(string(b), `"next"`) {
		t.Fatalf("JSON：%s", b)
	}
}
