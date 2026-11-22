package appapi

// docs/62 K1：扩展命令取当前装配的服务（App.Service）、插件不经 App 发扩展事件（EmitOn）。

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/runtime/ext"
)

type termMgr struct{ name string }

var keyTerm = ext.NewKey[*termMgr]("service:x.terminal")

func TestExtensionService(t *testing.T) {
	mgr := &termMgr{name: "cur"}
	vals := map[string]any{keyTerm.Name(): mgr}
	d := newDemo()
	d.cmds["x.demo.svc"] = ExtCommand{Handler: Bind(func(_ context.Context, c ExtCall, _ wire.Empty) (map[string]any, error) {
		v, ok := c.App.Service(keyTerm.Name())
		m, _ := v.(*termMgr)
		_, missOK := c.App.Service("service:x.none")
		out := map[string]any{"ok": ok, "miss": missOK}
		if m != nil {
			out["name"] = m.name
		}
		return out, nil
	})}
	app := newAppOpts(t, func(o *Options) {
		o.Extensions = []Extension{d}
		o.Services = func(k string) (any, bool) { v, ok := vals[k]; return v, ok }
	})
	r, e := call2(t, app, "x.demo.svc", `{}`)
	if e != nil {
		t.Fatal(e)
	}
	got := r.(map[string]any)
	if got["ok"] != true || got["name"] != "cur" || got["miss"] != false {
		t.Fatalf("Service：%v", got)
	}

	// 没接装配：nil, false
	bare := newApp(t)
	if v, ok := bare.Service(keyTerm.Name()); v != nil || ok {
		t.Fatalf("没接装配应取不到：%v %v", v, ok)
	}
}

func TestEmitOnSessionRouting(t *testing.T) {
	app := newAppOpts(t, func(o *Options) { o.Extensions = []Extension{newDemo()} })
	for _, s := range []string{"s1", "s2"} {
		if _, e := call2(t, app, "session.create", `{"session":"`+s+`"}`); e != nil {
			t.Fatal(e)
		}
	}
	zero := int64(0)
	c1 := app.NewConn(context.Background(), LocalPrincipal)
	defer c1.Close()
	c2 := app.NewConn(context.Background(), LocalPrincipal)
	defer c2.Close()
	if err := c1.Subscribe(context.Background(), wire.SubscribeArgs{Session: "s1", After: &zero}); err != nil {
		t.Fatal(err)
	}
	if err := c2.Subscribe(context.Background(), wire.SubscribeArgs{Session: "s2", After: &zero}); err != nil {
		t.Fatal(err)
	}
	waitKind(t, c1, wire.EvSynced)
	waitKind(t, c2, wire.EvSynced)
	sub := app.Subscribe(context.Background(), "")
	defer sub.Close()

	b := app.Bus()                          // 插件侧：host.Bus
	EmitOn(b, "s1", "y.terminal.output", 1) // 未登记命名空间：被拒
	EmitOn(b, "s1", "x.", 1)                // 只有前缀：被拒
	EmitOn(b, "s1", "x.terminal.output", map[string]string{"chunk": "hi"})
	EmitOn(b, "", "x.demo.marker", 0) // 全局：推所有连接
	EmitOn(nil, "s1", "x.terminal.output", 0)

	got1 := waitKind(t, c1, "x.demo.marker")
	got2 := waitKind(t, c2, "x.demo.marker")
	k1, k2 := kindsOf(got1), kindsOf(got2)
	if !slices.Contains(k1, "x.terminal.output") {
		t.Fatalf("订阅 s1 的连接应收到会话事件：%v", k1)
	}
	var data any
	for _, e := range got1 {
		if e.Kind == "x.terminal.output" {
			data = e.Data
			if e.Session != "s1" || e.Cursor != 0 {
				t.Fatalf("易失、带会话：%+v", e)
			}
		}
	}
	if m, _ := data.(map[string]string); m["chunk"] != "hi" {
		t.Fatalf("data：%#v", data)
	}
	if slices.Contains(k2, "x.terminal.output") {
		t.Fatalf("没订阅 s1 的连接不应收到：%v", k2)
	}
	for _, k := range append(k1, k2...) {
		if k == "y.terminal.output" || k == "x." {
			t.Fatalf("未登记命名空间的事件不应推出：%v / %v", k1, k2)
		}
	}

	// 进程内订阅同一条校验
	var in []string
	tm := time.After(5 * time.Second)
	for !slices.Contains(in, "x.demo.marker") {
		select {
		case ev := <-sub.C():
			in = append(in, ev.Kind)
		case <-tm:
			t.Fatalf("进程内订阅没等到 marker：%v", in)
		}
	}
	if slices.Contains(in, "y.terminal.output") || slices.Contains(in, "x.") || !slices.Contains(in, "x.terminal.output") {
		t.Fatalf("进程内订阅：%v", in)
	}

	// 不入事件表：s1 回放里没有扩展事件
	c3 := app.NewConn(context.Background(), LocalPrincipal)
	defer c3.Close()
	if err := c3.Subscribe(context.Background(), wire.SubscribeArgs{Session: "s1", After: &zero}); err != nil {
		t.Fatal(err)
	}
	if k := kindsOf(waitKind(t, c3, wire.EvSynced)); slices.Contains(k, "x.terminal.output") {
		t.Fatalf("扩展事件不应进事件表：%v", k)
	}
}
