package apphost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/appapi"
	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/runtime/bus"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/host"
	"github.com/Sevyn17/tenon/runtime/store/memstore"
)

// plugins.set 经真实文件：用户层写入、pending、if_revision 冲突、项目未受信不可写、强制插件、未知键、项目层禁设的键。
func TestPluginsSetEndToEnd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TENON_HOME", home)
	root := t.TempDir()
	p, _, err := LoadProfiles("", "", root, false)
	if err != nil {
		t.Fatal(err)
	}
	pr := Profiles{Root: root, Trusted: false}
	call := func(a *appapi.App, cmd string, args any) (any, *appapi.Error) {
		b, _ := json.Marshal(args)
		out, err := a.Call(context.Background(), cmd, b)
		var e *appapi.Error
		if err != nil && !errors.As(err, &e) {
			t.Fatalf("%s：%v", cmd, err)
		}
		return out, e
	}
	a := newPluginsApp(t, p, pr)

	out, e := call(a, "plugins.list", wire.Empty{})
	if e != nil {
		t.Fatal(e)
	}
	l := out.(wire.PluginsListResult)
	if len(l.Scopes) != 2 || !l.Scopes[0].Writable || l.Scopes[0].Revision != "" || l.Scopes[1].Writable {
		t.Fatalf("scopes：%+v", l.Scopes)
	}
	var compact wire.PluginInfo
	for _, x := range l.Plugins {
		if x.Name == "compact-basic" {
			compact = x
		}
		if x.Name == "store-sqlite" && (x.Mandatory || !x.On) {
			t.Fatalf("store-sqlite 不再锁定，但缺省开着：%+v", x)
		}
	}
	if compact.Name == "" || compact.Pending != nil {
		t.Fatalf("compact-basic：%+v", compact)
	}

	empty := ""
	off := false
	out, e = call(a, "plugins.set", wire.PluginsSetArgs{Name: "compact-basic", Scope: "user", Enabled: &off,
		Config: map[string]any{"headroom": 1000}, IfRevision: &empty})
	if e != nil {
		t.Fatal(e)
	}
	r := out.(wire.PluginsSetResult)
	if !r.Restart || r.Revision == "" || r.Plugin.Pending == nil || r.Plugin.Pending.On || fmt.Sprint(r.Plugin.Pending.Config["headroom"]) != "1000" {
		t.Fatalf("结果：%+v pending=%+v", r, r.Plugin.Pending)
	}
	b, _ := os.ReadFile(filepath.Join(home, "profile.toml"))
	if !strings.Contains(string(b), "compact-basic") || !strings.Contains(string(b), "headroom = 1000") {
		t.Fatalf("文件：\n%s", b)
	}
	// 旧 revision 再写：conflict stale
	_, e = call(a, "plugins.set", wire.PluginsSetArgs{Name: "compact-basic", Scope: "user", Enabled: &off, IfRevision: &empty})
	if e == nil || e.Code != wire.CodeConflict || e.Details["reason"] != wire.ConflictStale {
		t.Fatalf("应冲突：%+v", e)
	}
	// 改回去：pending 消失
	on := true
	_, e = call(a, "plugins.set", wire.PluginsSetArgs{Name: "compact-basic", Scope: "user", Enabled: &on,
		Config: map[string]any{"headroom": nil}, IfRevision: &r.Revision})
	if e != nil {
		t.Fatal(e)
	}
	out, _ = call(a, "plugins.list", wire.Empty{})
	for _, x := range out.(wire.PluginsListResult).Plugins {
		if x.Name == "compact-basic" && x.Pending != nil && (x.Pending.On != x.On || len(x.Pending.Config) != 0) {
			t.Fatalf("改回后不应有差别：%+v", x.Pending)
		}
	}

	for _, c := range []struct {
		args wire.PluginsSetArgs
		code string
	}{
		{wire.PluginsSetArgs{Name: "compact-basic", Scope: "project", Enabled: &on}, wire.CodeDenied},
		{wire.PluginsSetArgs{Name: "store-sqlite", Scope: "user", Enabled: &off}, wire.CodeInvalid},
		{wire.PluginsSetArgs{Name: "compact-basic", Scope: "user", Config: map[string]any{"nope": 1}}, wire.CodeInvalid},
		{wire.PluginsSetArgs{Name: "compact-basic", Scope: "user", Config: map[string]any{"headroom": "x"}}, wire.CodeInvalid},
		{wire.PluginsSetArgs{Name: "no-such", Scope: "user", Enabled: &on}, wire.CodeNotFound},
		{wire.PluginsSetArgs{Name: "compact-basic", Scope: "x", Enabled: &on}, wire.CodeInvalid},
	} {
		if _, e := call(a, "plugins.set", c.args); e == nil || e.Code != c.code {
			t.Errorf("%+v 应为 %s：%+v", c.args, c.code, e)
		}
	}

	// 受信项目：项目层可写，但不能设端点等键
	trusted := newPluginsApp(t, p, Profiles{Root: root, Trusted: true})
	if _, e := call(trusted, "plugins.set", wire.PluginsSetArgs{Name: "models", Scope: "project", Config: map[string]any{"base_url": "http://x"}}); e == nil || e.Code != wire.CodeDenied {
		t.Fatalf("项目层设 base_url 应 denied：%+v", e)
	}
	if _, e := call(trusted, "plugins.set", wire.PluginsSetArgs{Name: "compact-basic", Scope: "project", Enabled: &off}); e != nil {
		t.Fatal(e)
	}
	if _, err := os.Stat(filepath.Join(root, ".tenon", "profile.toml")); err != nil {
		t.Fatal(err)
	}
}

// newPluginsApp 只为插件命令建一个 App（内存存储，不起会话）。
func newPluginsApp(t *testing.T, p *component.Profile, pr Profiles) *appapi.App {
	t.Helper()
	b := bus.New(0)
	t.Cleanup(b.Close)
	a, err := appapi.New(appapi.Options{Host: &host.Host{}, Bus: b, DB: memstore.New(), Profile: p, Profiles: pr})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// 存储不锁定是哪个插件，但 plugins.set 不能把最后一个提供 service:db 的插件关掉（关了下次启动就起不来）。
func TestPluginsSetKeepsAStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TENON_HOME", home)
	root := t.TempDir()
	p, _, err := LoadProfiles("", "", root, false)
	if err != nil {
		t.Fatal(err)
	}
	a := newPluginsApp(t, p, Profiles{Root: root})
	off := false
	b, _ := json.Marshal(wire.PluginsSetArgs{Name: "store-sqlite", Scope: "user", Enabled: &off})
	_, err = a.Call(context.Background(), "plugins.set", b)
	var e *appapi.Error
	if !errors.As(err, &e) || e.Code != wire.CodeInvalid {
		t.Fatalf("关掉唯一的存储插件应报 invalid：%v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "profile.toml")); err == nil {
		t.Fatal("被拒绝时不应写文件")
	}
}
