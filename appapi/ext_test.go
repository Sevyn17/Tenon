package appapi

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/appapi/wire"
)

// demoExt 示例扩展：命名空间 x，命令 x.demo.echo（viewer 可调）与 x.demo.write（只给 operator）。
type demoExt struct {
	ns   string
	cmds map[string]ExtCommand
}

type echoArgs struct {
	Text string `json:"text"`
}

func (d demoExt) Namespace() string               { return d.ns }
func (d demoExt) Commands() map[string]ExtCommand { return d.cmds }
func (d demoExt) Capabilities() []string          { return []string{"x.demo"} }
func (d demoExt) Version() string                 { return "1.0" }

func newDemo() demoExt {
	return demoExt{ns: "x", cmds: map[string]ExtCommand{
		"x.demo.echo": {Viewer: true, Handler: Bind(func(_ context.Context, c ExtCall, in echoArgs) (echoArgs, error) {
			if in.Text == "" {
				return echoArgs{}, Errorf(wire.CodeInvalid, "empty")
			}
			return echoArgs{Text: in.Text + "@" + c.Principal.ID}, nil
		})},
		"x.demo.write": {Handler: Bind(func(context.Context, ExtCall, wire.Empty) (wire.Empty, error) { return wire.Empty{}, nil })},
	}}
}

func TestExtensionCommandsAndHello(t *testing.T) {
	app := newAppOpts(t, func(o *Options) { o.Extensions = []Extension{newDemo()} })
	r, e := call2(t, app, "x.demo.echo", `{"text":"hi"}`)
	if e != nil || r.(echoArgs).Text != "hi@local" {
		t.Fatalf("进程内调用：%+v %v", r, e)
	}
	_, e = call2(t, app, "x.demo.echo", `{}`)
	wantCode(t, e, wire.CodeInvalid, "")
	_, e = call2(t, app, "x.demo.echo", `{bad`)
	wantCode(t, e, wire.CodeBadRequest, "")
	if !slices.Equal(app.ExtCommands(), []string{"x.demo.echo", "x.demo.write"}) {
		t.Fatalf("ExtCommands = %v", app.ExtCommands())
	}
	if slices.Contains(Commands(), "x.demo.echo") || slices.Contains(ConnCommands(), "x.demo.echo") {
		t.Fatal("Commands / ConnCommands 只列核心命令")
	}
	h, e := call2(t, app, "hello", `{}`)
	if e != nil {
		t.Fatal(e)
	}
	hr := h.(wire.HelloResult)
	if !slices.Contains(hr.Capabilities, "x.demo") || len(hr.Extensions) != 1 || hr.Extensions[0] != (wire.ExtensionInfo{Namespace: "x", Version: "1.0"}) {
		t.Fatalf("hello: %+v", hr)
	}

	// 连接上：viewer 只能调标了 Viewer 的扩展命令
	v := app.NewConn(context.Background(), wire.Principal{ID: "v1", Role: wire.RoleViewer})
	defer v.Close()
	out, err := v.Call(context.Background(), "x.demo.echo", json.RawMessage(`{"text":"a"}`))
	if err != nil || out.(echoArgs).Text != "a@v1" {
		t.Fatalf("viewer echo: %+v %v", out, err)
	}
	_, err = v.Call(context.Background(), "x.demo.write", nil)
	var ae *Error
	if !errors.As(err, &ae) || ae.Code != wire.CodeForbidden {
		t.Fatalf("viewer 调 operator 命令应 forbidden：%v", err)
	}
	op := app.NewConn(context.Background(), LocalPrincipal)
	defer op.Close()
	if _, err := op.Call(context.Background(), "x.demo.write", nil); err != nil {
		t.Fatal(err)
	}
}

// 没有扩展时 hello 不带 extensions（旧客户端看到的不变）。
func TestExtensionHelloUnchangedWithout(t *testing.T) {
	app := newApp(t)
	h, _ := call2(t, app, "hello", `{}`)
	b, _ := json.Marshal(h)
	if strings.Contains(string(b), "extensions") {
		t.Fatalf("没有扩展时不应带 extensions：%s", b)
	}
}

func TestExtensionRules(t *testing.T) {
	h := Bind(func(context.Context, ExtCall, wire.Empty) (wire.Empty, error) { return wire.Empty{}, nil })
	one := func(ns, cmd string) Extension { return demoExt{ns: ns, cmds: map[string]ExtCommand{cmd: {Handler: h}}} }
	bad := [][]Extension{
		{one("x", "y.demo.a")},                   // 不带自己的命名空间
		{one("x", "x.")},                         // 只有前缀
		{one("session", "session.foo")},          // 命名空间是保留根
		{one("terminal", "terminal.open")},       // B 卡保留根
		{one("platform", "platform.x")},          // 命名空间与历史产品根冲突
		{one("models", "models.providers.list")}, // 保留根
		{one("a.b", "a.b.c")},                    // 命名空间含点
		{one("", "x")},                           // 空命名空间
		{one("x", "platform.newThing"), one("y", "platform.newThing")}, // 两个扩展同名
		{one("x", "x.a"), one("x", "x.b")},                             // 命名空间重复
		{demoExt{ns: "x", cmds: map[string]ExtCommand{"x.a": {}}}},     // 没有处理函数
	}
	for i, exts := range bad {
		if _, err := buildExt(exts); err == nil {
			t.Errorf("第 %d 组应被拒绝", i)
		}
	}
	// 核心命令同名（platform.status 当前仍是核心命令，T1 迁出后由扩展注册）
	if _, err := buildExt([]Extension{one("x", "platform.status")}); err == nil {
		t.Error("与核心命令同名应被拒绝")
	}
	good := []Extension{one("mortise", "mortise.system.storage"), one("y", "platform.newThing"), one("z", "models.providers.extra")}
	if _, err := buildExt(good); err != nil {
		t.Fatalf("合法扩展被拒：%v", err)
	}
	// New 在冲突时失败
	db := newApp(t)
	o := db.o
	o.Extensions = []Extension{one("x", "y.a")}
	if _, err := New(o); err == nil {
		t.Fatal("New 应在扩展不合法时失败")
	}
}

func TestExtensionEmit(t *testing.T) {
	app := newAppOpts(t, func(o *Options) { o.Extensions = []Extension{newDemo()} })
	if err := app.Emit("", "mortise.x", 1); !errors.Is(err, ErrExtKind) {
		t.Fatalf("不带已注册命名空间应失败：%v", err)
	}
	if err := app.Emit("", "x.", 1); !errors.Is(err, ErrExtKind) {
		t.Fatalf("只有前缀应失败：%v", err)
	}
	c := app.NewConn(context.Background(), LocalPrincipal)
	defer c.Close()
	sub := app.Subscribe(context.Background(), "", "x.demo.changed")
	defer sub.Close()
	if err := app.Emit("", "x.demo.changed", map[string]int{"revision": 2}); err != nil {
		t.Fatal(err)
	}
	waitKind(t, c, "x.demo.changed")
	select {
	case ev := <-sub.C():
		if ev.Kind != "x.demo.changed" {
			t.Fatalf("进程内订阅收到 %s", ev.Kind)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("进程内订阅没收到扩展事件")
	}
}

// 进程内订阅能收到回合结束（docs/57 4.2 钩子 2）。
func TestSubscribeTurnEnded(t *testing.T) {
	app := newApp(t)
	if _, e := call2(t, app, "session.create", `{"session":"s"}`); e != nil {
		t.Fatal(e)
	}
	sub := app.Subscribe(context.Background(), "s", wire.EvTurnEnded)
	if _, e := call2(t, app, "session.send", `{"session":"s","text":"hi","client_id":"c1"}`); e != nil {
		t.Fatal(e)
	}
	select {
	case ev, ok := <-sub.C():
		if !ok || ev.Kind != wire.EvTurnEnded || ev.Session != "s" {
			t.Fatalf("收到 %+v %v", ev, ok)
		}
		if te, _ := ev.Data.(wire.TurnEnded); te.Kind != "done" {
			t.Fatalf("turn.ended 载荷：%+v", ev.Data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("没收到 turn.ended")
	}
	sub.Close()
	for range sub.C() { // Close 后通道关闭
	}
}

// session.create {source, ref, title}（docs/57 4.2 钩子 3）。
func TestCreateSourceRefTitle(t *testing.T) {
	app := newApp(t)
	r, e := call2(t, app, "session.create", `{"session":"a","source":"automation","ref":"task-1","title":"  每日 · 11-22 08:00 "}`)
	if e != nil {
		t.Fatal(e)
	}
	s := r.(wire.SessionSnapshot)
	if s.Source != "automation" || s.Ref != "task-1" || s.Title != "每日 · 11-22 08:00" {
		t.Fatalf("快照：%+v", s)
	}
	if _, e := call2(t, app, "session.send", `{"session":"a","text":"别的标题","client_id":"c1"}`); e != nil {
		t.Fatal(e)
	}
	g, _ := call2(t, app, "session.get", `{"session":"a"}`)
	if g.(wire.SessionSnapshot).Title != "每日 · 11-22 08:00" {
		t.Fatalf("有初始标题时不应被第一条消息覆盖：%q", g.(wire.SessionSnapshot).Title)
	}
	l, _ := call2(t, app, "sessions.list", `{}`)
	items := l.(wire.ListResult).Items
	if len(items) != 1 || items[0].Source != "automation" || items[0].Ref != "task-1" {
		t.Fatalf("列表行：%+v", items)
	}
	for _, bad := range []string{`{"source":"a\nb"}`, `{"ref":"` + strings.Repeat("x", 129) + `"}`, `{"title":"a\nb"}`} {
		_, e := call2(t, app, "session.create", bad)
		wantCode(t, e, wire.CodeInvalid, "")
	}
	// 不带新字段时行为不变
	r, e = call2(t, app, "session.create", `{"session":"b"}`)
	if e != nil || r.(wire.SessionSnapshot).Source != "" || r.(wire.SessionSnapshot).Title != "" {
		t.Fatalf("%+v %v", r, e)
	}
}

// Rebuild：没有管理器什么也不做；有回合在跑不带 force 返回 not_idle 并调 undo。
func TestExtensionRebuild(t *testing.T) {
	app := newApp(t)
	if err := app.Rebuild(context.Background(), false, nil); err != nil {
		t.Fatal(err)
	}
	rl := &fakeReloader{}
	app.o.Workspaces = rl
	if err := app.Rebuild(context.Background(), false, nil); err != nil || rl.reloads != 1 {
		t.Fatalf("%v %d", err, rl.reloads)
	}
	rl.err = errors.New("坏了")
	undone := 0
	err := app.Rebuild(context.Background(), false, func() error { undone++; return nil })
	var ae *Error
	if !errors.As(err, &ae) || ae.Code != wire.CodeInternal || undone != 1 {
		t.Fatalf("重建失败应 internal 并 undo：%v %d", err, undone)
	}
	rl.err = &ReloadReverted{Err: errors.New("x")}
	err = app.Rebuild(context.Background(), false, func() error { undone++; return nil })
	if !errors.As(err, &ae) || ae.Code != wire.CodeInvalid || ae.Details["reverted"] != true || undone != 2 {
		t.Fatalf("回到原配置应 invalid+reverted：%v", err)
	}
}
