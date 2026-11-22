package appapi

// 客户端改进 M10 T19：notice 连接级送达（不订阅会话也收得到）、带 level / source、第一次 hello 后补发最近 20 条；
// mode.list 的 default 与 description（M10 T17 / 缺省档位）。

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/runtime/bus"
	"github.com/Sevyn17/tenon/runtime/host"
	"github.com/Sevyn17/tenon/runtime/store/memstore"
)

// noticesOf 从连接取事件，直到 d 内没有新事件；返回其中的 notice。
func noticesOf(t *testing.T, c *Conn, d time.Duration) []wire.Envelope {
	t.Helper()
	var out []wire.Envelope
	for {
		ctx, cancel := context.WithTimeout(context.Background(), d)
		evs, err := c.Next(ctx)
		cancel()
		if err != nil {
			return out
		}
		for _, e := range evs {
			if e.Kind == wire.EvNotice {
				out = append(out, e)
			}
		}
	}
}

func noticeText(e wire.Envelope) string { return e.Data.(wire.Notice).Text }

// 装配期的告警（UI.Warn，早于 App）与连接之前的提示在第一次 hello 后补发，带 replay；之后的实时推送，不重复。
func TestNoticeReplayAfterHello(t *testing.T) {
	db := memstore.New()
	b := bus.New(0)
	h := &host.Host{DB: db, Bus: b}
	t.Cleanup(func() { h.Close(); b.Close(); db.Close() })
	ui := NewUI(b)
	ui.Warn("MCP", "服务 x 起不来") // 装配期：App 还没建
	app, err := New(Options{Host: h, Bus: b, DB: db, Model: "m", Workdir: "w", UI: ui})
	if err != nil {
		t.Fatal(err)
	}
	app.NoticeFrom("", wire.NoticeError, "ready", "ready: 工作组补投失败") // workspace.start 里、连接之前

	c := app.NewConn(context.Background(), LocalPrincipal)
	defer c.Close()
	if got := noticesOf(t, c, 100*time.Millisecond); len(got) != 0 {
		t.Fatalf("hello 之前不该补发：%v", got)
	}
	r, err := c.Call(context.Background(), "hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(r.(wire.HelloResult).Capabilities, CapNotices) {
		t.Fatalf("hello 应报 %s", CapNotices)
	}
	got := noticesOf(t, c, 200*time.Millisecond)
	if len(got) != 2 {
		t.Fatalf("应补发 2 条：%+v", got)
	}
	w, e := got[0].Data.(wire.Notice), got[1].Data.(wire.Notice)
	if w.Level != wire.NoticeWarn || w.Source != "MCP" || w.Text != "[MCP] 服务 x 起不来" || !w.Replay {
		t.Fatalf("插件告警：%+v", w)
	}
	if e.Level != wire.NoticeError || e.Source != "ready" || !e.Replay || got[1].Session != "" || got[1].At == 0 {
		t.Fatalf("ready 错误：%+v %+v", e, got[1])
	}

	// 再 hello 不再补发；之后的提示实时到，不订阅会话也收得到，不带 replay。
	if _, err := c.Call(context.Background(), "hello", nil); err != nil {
		t.Fatal(err)
	}
	app.NoticeFrom("s1", wire.NoticeInfo, "permission", "[权限] 档位 x 的配置已更新")
	got = noticesOf(t, c, 200*time.Millisecond)
	if len(got) != 1 || got[0].Session != "s1" || got[0].Data.(wire.Notice).Replay || got[0].Data.(wire.Notice).Source != "permission" {
		t.Fatalf("实时提示：%+v", got)
	}
}

// 只保留最近 20 条；订阅了会话也只收一遍（会话订阅不再推 notice）。
func TestNoticeKeepAndNoDuplicate(t *testing.T) {
	app := newApp(t)
	for i := range 25 {
		app.Notice("", fmt.Sprint("n", i))
	}
	c := app.NewConn(context.Background(), LocalPrincipal)
	defer c.Close()
	if _, err := c.Call(context.Background(), "hello", nil); err != nil {
		t.Fatal(err)
	}
	got := noticesOf(t, c, 200*time.Millisecond)
	if len(got) != wire.NoticeKeep || noticeText(got[0]) != "n5" || noticeText(got[19]) != "n24" {
		t.Fatalf("应补发最近 %d 条：%d 条，首条 %v", wire.NoticeKeep, len(got), got)
	}
	if got[0].Data.(wire.Notice).Level != wire.NoticeInfo {
		t.Fatalf("Notice 缺省级别应为 info：%+v", got[0].Data)
	}

	if _, e := call2(t, app, "session.create", `{"session":"s"}`); e != nil {
		t.Fatal(e)
	}
	if err := c.Subscribe(context.Background(), wire.SubscribeArgs{Session: "s"}); err != nil {
		t.Fatal(err)
	}
	waitKind(t, c, wire.EvSynced)
	app.Notice("s", "只一遍")
	got = noticesOf(t, c, 200*time.Millisecond)
	if len(got) != 1 || noticeText(got[0]) != "只一遍" {
		t.Fatalf("订阅了的会话的提示应只收一遍：%+v", got)
	}
}

// fakeModes 档位：Notice 每次返回 next 后清空；实现 ModeDefaulter。
type fakeModes struct {
	mu   sync.Mutex
	next string
	def  string
}

func (m *fakeModes) List() []ModeInfo {
	return []ModeInfo{{ID: "ask", Label: "每次询问", Builtin: true, Description: "每次都问"},
		{ID: "workspace", Label: "工作区允许", Builtin: true, Description: "工作区内直接做"}}
}
func (m *fakeModes) Get(string) ModeState { return ModeState{Mode: "ask", Label: "每次询问"} }
func (m *fakeModes) Set(_, mode string, _ bool) (ModeState, error) {
	return ModeState{Mode: mode, Label: mode}, nil
}
func (m *fakeModes) Show(string, string) ([]ModeItem, error) { return nil, nil }
func (m *fakeModes) Notice(string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := m.next
	m.next = ""
	return n
}
func (m *fakeModes) SessionRules(string) []string                         { return nil }
func (m *fakeModes) SessionAllow(string, json.RawMessage) (r, b []string) { return nil, nil }
func (m *fakeModes) AllowForSession(string, string, json.RawMessage) (a, all []string) {
	return nil, nil
}
func (m *fakeModes) ExtKeys() (string, string) { return "permission.mode", "permission.rules" }
func (m *fakeModes) DefaultMode() string       { return m.def }

// 档位文件变了：下一回合开始时发 notice（source=permission），没订阅这个会话的连接也收得到。
func TestModeNoticeReachesConn(t *testing.T) {
	fm := &fakeModes{}
	app2 := newAppOpts(t, func(o *Options) { o.Modes = fm }) // BeforeTurn 包装在 New 里读 Options.Modes，须构造时给
	c := app2.NewConn(context.Background(), LocalPrincipal)
	defer c.Close()
	if _, err := c.Call(context.Background(), "hello", nil); err != nil {
		t.Fatal(err)
	}
	if _, e := call2(t, app2, "session.create", `{"session":"s"}`); e != nil {
		t.Fatal(e)
	}
	fm.mu.Lock()
	fm.next = "[权限] 档位 ask 的配置已更新，本回合起生效"
	fm.mu.Unlock()
	sendWait(t, app2, "s", "hi", "c1")
	got := noticesOf(t, c, 300*time.Millisecond)
	if len(got) != 1 {
		t.Fatalf("应收到 1 条档位提示：%+v", got)
	}
	n := got[0].Data.(wire.Notice)
	if got[0].Session != "s" || n.Source != "permission" || n.Level != wire.NoticeInfo || n.Replay {
		t.Fatalf("档位提示：%+v %+v", got[0], n)
	}
	if modeNoticeLevel("", "[permission] failed to read mode files, keeping previous config: x") != wire.NoticeWarn ||
		modeNoticeLevel("", "[permission] mode x no longer exists; using ask from this turn") != wire.NoticeWarn ||
		modeNoticeLevel(NoticeKeyModeGone, "") != wire.NoticeWarn || modeNoticeLevel(NoticeKeyModeUpdated, "failed") != wire.NoticeInfo {
		t.Fatal("读取失败、档位不存在应是 warn；有键时按键判断")
	}
}

// keyedModes 实现 ModeNoticer：档位提示带稳定键与参数。
type keyedModes struct{ fakeModes }

func (m *keyedModes) NoticeOf(string) (string, map[string]string, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := m.next
	m.next = ""
	if n == "" {
		return "", nil, ""
	}
	return NoticeKeyModeGone, map[string]string{"mode": "x"}, n
}

// 档位提示带 key / args 上线（T3：产品按键翻译，text 是英文兜底）。
func TestModeNoticeKey(t *testing.T) {
	km := &keyedModes{}
	app2 := newAppOpts(t, func(o *Options) { o.Modes = km })
	c := app2.NewConn(context.Background(), LocalPrincipal)
	defer c.Close()
	if _, err := c.Call(context.Background(), "hello", nil); err != nil {
		t.Fatal(err)
	}
	if _, e := call2(t, app2, "session.create", `{"session":"s"}`); e != nil {
		t.Fatal(e)
	}
	km.mu.Lock()
	km.next = "[permission] mode x no longer exists; using ask from this turn"
	km.mu.Unlock()
	sendWait(t, app2, "s", "hi", "c1")
	got := noticesOf(t, c, 300*time.Millisecond)
	if len(got) != 1 {
		t.Fatalf("应收到 1 条档位提示：%+v", got)
	}
	n := got[0].Data.(wire.Notice)
	if n.Key != NoticeKeyModeGone || n.Args["mode"] != "x" || n.Level != wire.NoticeWarn || n.Text == "" {
		t.Fatalf("档位提示应带键与参数：%+v", n)
	}
}

// mode.list：每行带 description；default 取 ModeDefaulter，且只给 modes 里有的 id。
func TestModeListDefault(t *testing.T) {
	app := newApp(t)
	fm := &fakeModes{def: "workspace"}
	app.o.Modes = fm
	out, e := call2(t, app, "mode.list", `{}`)
	if e != nil {
		t.Fatal(e)
	}
	r := out.(wire.ModeListResult)
	if r.Default != "workspace" || r.Modes[0].Description != "每次都问" {
		t.Fatalf("mode.list：%+v", r)
	}
	fm.def = "gone"
	out, _ = call2(t, app, "mode.list", `{}`)
	if d := out.(wire.ModeListResult).Default; d != "" {
		t.Fatalf("不在列表里的缺省档位不该给：%q", d)
	}
}
