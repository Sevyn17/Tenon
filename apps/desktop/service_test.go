package main

// 绑定服务测试：用最小宿主（假模型、sqlite 临时库）驱动 Service.Call，并检查事件转发；
// 另校验前端 web/src/client/commands.ts 的命令表与 appapi.ConnCommands() 一致。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"tenon/appapi"
	"tenon/appapi/wire"
	"tenon/runtime/agent"
	"tenon/runtime/bus"
	"tenon/runtime/host"
	"tenon/runtime/session"
	"tenon/runtime/store/sqlite"
	"tenon/runtime/toolhub"
)

type fakeModel struct{}

func (fakeModel) Stream(_ context.Context, req session.Request, onText func(string)) (session.AssistantItem, session.StopReason, error) {
	last := req.Messages[len(req.Messages)-1]
	txt := ""
	for _, p := range last.Content {
		txt += p.Text
	}
	out := "re:" + txt
	if onText != nil {
		onText(out)
	}
	return session.AssistantItem{Content: session.Content{session.NewTextPart(out)}}, session.StopReason{V: "stop"}, nil
}

func newTestApp(t *testing.T) *appapi.App {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "s.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	b := bus.New(0)
	t.Cleanup(func() { b.Close(); db.Close() })
	var app *appapi.App
	h := &host.Host{DB: db, Bus: b}
	h.New = func(id, def string) (*agent.Agent, *session.Config, error) {
		hub := toolhub.New(toolhub.Options{Store: agent.ToolStore{DB: db}})
		a := &agent.Agent{Store: db, SessionID: id, Model: fakeModel{}, Hub: hub, Hooks: app.StreamHooks(id)}
		cfg := &session.Config{Model: "m", Tools: hub.Exposed(hub.SpecsFor(nil, 0)), Policy: session.DefaultPolicy()}
		return a, cfg, nil
	}
	app, err = appapi.New(appapi.Options{Host: h, Bus: b, DB: db, Model: "m", Workdir: "w", UI: appapi.NewUI(b)})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

type recorder struct {
	mu  sync.Mutex
	evs []wire.Envelope
}

func (r *recorder) emit(name string, data any) {
	if name != EventName {
		return
	}
	if ev, ok := data.(wire.Envelope); ok {
		r.mu.Lock()
		r.evs = append(r.evs, ev)
		r.mu.Unlock()
	}
}

func (r *recorder) wait(t *testing.T, kind string) wire.Envelope {
	t.Helper()
	dl := time.Now().Add(30 * time.Second) // 全量并行跑时机器负载高，5s 会偶发超时
	for time.Now().Before(dl) {
		r.mu.Lock()
		for _, e := range r.evs {
			if e.Kind == kind {
				r.mu.Unlock()
				return e
			}
		}
		r.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("没等到事件 %s", kind)
	return wire.Envelope{}
}

func TestServiceCall(t *testing.T) {
	rec := &recorder{}
	svc := NewService(newTestApp(t), rec.emit)
	svc.StartEvents()
	defer svc.StopEvents()

	out, err := svc.Call("hello", `{"client":"test","protocol":{"min":2,"max":2}}`)
	if err != nil {
		t.Fatal(err)
	}
	var hr wire.HelloResult
	if err := json.Unmarshal([]byte(out), &hr); err != nil || hr.Protocol != wire.Protocol || hr.Model != "m" ||
		hr.Principal != appapi.LocalPrincipal {
		t.Fatalf("hello: %s %v", out, err)
	}

	// 错误：未知命令 → unsupported；非法 JSON → bad_request；会话不存在 → not_found（不隐式新建）。
	for _, c := range []struct{ cmd, args, want string }{
		{"nope", "", wire.CodeUnsupported},
		{"hello", "{bad", wire.CodeBadRequest},
		{"session.get", `{"session":"none"}`, wire.CodeNotFound},
	} {
		_, err := svc.Call(c.cmd, c.args)
		var e wire.Error
		if err == nil || json.Unmarshal([]byte(err.Error()), &e) != nil || e.Code != c.want {
			t.Fatalf("%s: 应为 %s：%v", c.cmd, c.want, err)
		}
	}

	if _, err := svc.Call("session.create", `{"session":"s1"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Call("session.subscribe", `{"session":"s1"}`); err != nil {
		t.Fatal(err)
	}
	rec.wait(t, wire.EvSynced)
	if _, err := svc.Call("session.send", `{"session":"s1","text":"hi","client_id":"c1"}`); err != nil {
		t.Fatal(err)
	}
	rec.wait(t, wire.EvTurnEnded)
	ev := rec.wait(t, wire.EvEntryAppended)
	if ev.Session != "s1" || ev.Cursor == 0 {
		t.Fatalf("entry.appended：%+v", ev)
	}

	out, err = svc.Call("session.entries", `{"session":"s1","tail":10}`)
	if err != nil {
		t.Fatal(err)
	}
	var er wire.EntriesResult
	if err := json.Unmarshal([]byte(out), &er); err != nil || len(er.Entries) < 2 || er.Entries[0].ClientIDs[0] != "c1" {
		t.Fatalf("entries: %s %v", out, err)
	}
	out, err = svc.Call("sessions.list", "")
	if err != nil || !strings.Contains(out, `"s1"`) {
		t.Fatalf("sessions.list: %s %v", out, err)
	}
}

// 前端命令表（web/src/client/commands.ts 的 COMMANDS）必须与连接上可用的命令（appapi.ConnCommands）一致。
func TestProtocolCommands(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "client", "commands.ts"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	i := strings.Index(s, "export const COMMANDS = [")
	if i < 0 {
		t.Fatal("commands.ts 里找不到 COMMANDS")
	}
	j := strings.Index(s[i:], "] as const")
	if j < 0 {
		t.Fatal("commands.ts 的 COMMANDS 没有结束")
	}
	var got []string
	for _, m := range regexp.MustCompile(`"([a-z_.]+)"`).FindAllStringSubmatch(s[i:i+j], -1) {
		got = append(got, m[1])
	}
	slices.Sort(got)
	if want := appapi.ConnCommands(); !slices.Equal(got, want) {
		t.Fatalf("commands.ts COMMANDS = %v\nappapi.ConnCommands() = %v", got, want)
	}
}
