package web

// 网页服务测试：令牌、Host / Origin 检查、hello 往返、事件推送、多连接独立订阅、监听地址规则。

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"golang.org/x/net/websocket"

	"github.com/Sevyn17/tenon/appapi"
	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/runtime/agent"
	"github.com/Sevyn17/tenon/runtime/bus"
	"github.com/Sevyn17/tenon/runtime/host"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store/memstore"
	"github.com/Sevyn17/tenon/runtime/toolhub"
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

func newTestApp(t *testing.T, opts ...func(*appapi.Options)) *appapi.App {
	t.Helper()
	db := memstore.New()
	t.Cleanup(func() { db.Close() })
	b := bus.New(0)
	t.Cleanup(func() { b.Close(); db.Close() })
	var app *appapi.App
	h := &host.Host{DB: db, Bus: b}
	h.New = func(id, def string) (*agent.Agent, *session.Config, error) {
		hub := toolhub.New(toolhub.Options{Store: agent.ToolStore{DB: db}})
		a := &agent.Agent{Store: db, SessionID: id, Model: fakeModel{}, Hub: hub, Hooks: app.StreamHooks(id)}
		cfg := &session.Config{Model: "m", Tools: hub.Exposed(hub.SpecsFor(nil)), Policy: session.DefaultPolicy()}
		return a, cfg, nil
	}
	o := appapi.Options{Host: h, Bus: b, DB: db, Model: "m", Workdir: "w", UI: appapi.NewUI(b)}
	for _, f := range opts {
		f(&o)
	}
	app, err := appapi.New(o)
	if err != nil {
		t.Fatal(err)
	}
	return app
}

const testToken = "test-token-0123456789abcdefghijklmnopqrstuv"

// newTestServer 起 httptest 服务；返回 http 基址（http://127.0.0.1:port）。
func newTestServer(t *testing.T) (string, *httptest.Server) {
	t.Helper()
	app := newTestApp(t)
	ts := httptest.NewUnstartedServer(nil)
	port := ts.Listener.Addr().(*net.TCPAddr).Port
	ts.Config.Handler = New(Config{API: app, Token: testToken, Hosts: LoopbackHosts(port),
		Static: fstest.MapFS{"index.html": {Data: []byte("<html>ok</html>")}}})
	ts.Start()
	t.Cleanup(ts.Close)
	return ts.URL, ts
}

func get(t *testing.T, u string, hdr map[string]string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", u, nil)
	for k, v := range hdr {
		if k == "Host" {
			req.Host = v
		} else {
			req.Header.Set(k, v)
		}
	}
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func TestStaticToken(t *testing.T) {
	base, _ := newTestServer(t)
	if r := get(t, base+"/", nil); r.StatusCode != 401 {
		t.Fatalf("无令牌：%d", r.StatusCode)
	}
	if r := get(t, base+"/?token=wrong", nil); r.StatusCode != 401 {
		t.Fatalf("错令牌：%d", r.StatusCode)
	}
	r := get(t, base+"/?token="+testToken, nil)
	if r.StatusCode != http.StatusSeeOther || r.Header.Get("Location") != "/?token=" {
		t.Fatalf("正确令牌应跳转：%d %q", r.StatusCode, r.Header.Get("Location"))
	}
	var ck *http.Cookie
	for _, c := range r.Cookies() {
		if c.Name == CookieName {
			ck = c
		}
	}
	if ck == nil || !ck.HttpOnly || ck.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie 不对：%+v", ck)
	}
	r = get(t, base+"/?token=", map[string]string{"Cookie": CookieName + "=" + testToken})
	if r.StatusCode != 200 {
		t.Fatalf("凭 cookie：%d", r.StatusCode)
	}
	csp := r.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("CSP：%q", csp)
	}
	if r.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("不应有 CORS 头")
	}
	if r := get(t, base+"/", map[string]string{"Authorization": "Bearer " + testToken}); r.StatusCode != 200 {
		t.Fatalf("Bearer：%d", r.StatusCode)
	}
}

func TestHostRejected(t *testing.T) {
	base, _ := newTestServer(t)
	u, _ := url.Parse(base)
	r := get(t, base+"/?token="+testToken, map[string]string{"Host": "evil.example:" + u.Port()})
	if r.StatusCode != 403 {
		t.Fatalf("重绑定 Host：%d", r.StatusCode)
	}
}

func wsConfig(t *testing.T, base, token, origin string) *websocket.Config {
	t.Helper()
	ws := "ws" + strings.TrimPrefix(base, "http") + "/ws?token=" + url.QueryEscape(token)
	cfg, err := websocket.NewConfig(ws, origin)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestWSRejected(t *testing.T) {
	base, _ := newTestServer(t)
	if _, err := websocket.DialConfig(wsConfig(t, base, "wrong", base)); err == nil {
		t.Fatal("错令牌应拒绝")
	}
	for _, o := range []string{"http://evil.example", "https" + strings.TrimPrefix(base, "http"), "http://localhost:1"} {
		if _, err := websocket.DialConfig(wsConfig(t, base, testToken, o)); err == nil {
			t.Fatalf("Origin %s 应拒绝", o)
		}
	}
	// 直接看状态码
	r := get(t, base+"/ws?token=wrong", map[string]string{"Origin": base, "Upgrade": "websocket", "Connection": "Upgrade"})
	if r.StatusCode != 401 {
		t.Fatalf("WS 错令牌：%d", r.StatusCode)
	}
	r = get(t, base+"/ws?token="+testToken, map[string]string{"Origin": "http://evil.example", "Upgrade": "websocket", "Connection": "Upgrade"})
	if r.StatusCode != 403 {
		t.Fatalf("WS 错 Origin：%d", r.StatusCode)
	}
	u, _ := url.Parse(base)
	r = get(t, base+"/ws?token="+testToken, map[string]string{"Host": "evil.example:" + u.Port(), "Origin": "http://evil.example:" + u.Port(), "Upgrade": "websocket", "Connection": "Upgrade"})
	if r.StatusCode != 403 {
		t.Fatalf("WS 错 Host：%d", r.StatusCode)
	}
}

type client struct {
	held []map[string]json.RawMessage // 等别的帧时读到、还没被取走的帧（按到达顺序）
	t    *testing.T
	ws   *websocket.Conn
	ch   chan map[string]json.RawMessage
}

func dial(t *testing.T, base string) *client {
	t.Helper()
	ws, err := websocket.DialConfig(wsConfig(t, base, testToken, base))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.Close() })
	c := &client{t: t, ws: ws, ch: make(chan map[string]json.RawMessage, 256)}
	go func() {
		defer close(c.ch)
		for {
			var s string
			if err := websocket.Message.Receive(ws, &s); err != nil {
				return
			}
			var m map[string]json.RawMessage
			if json.Unmarshal([]byte(s), &m) == nil {
				c.ch <- m
			}
		}
	}()
	return c
}

// next 取下一帧：先取等别的东西时攒下的帧，再从连接读。
func (c *client) next(dl <-chan time.Time) (map[string]json.RawMessage, bool) {
	if len(c.held) > 0 {
		m := c.held[0]
		c.held = c.held[1:]
		return m, true
	}
	select {
	case m, ok := <-c.ch:
		if !ok {
			c.t.Fatal("连接已关")
		}
		return m, true
	case <-dl:
		return nil, false
	}
}

// take 取第一个满足 match 的帧，途中不满足的帧按原顺序攒下给后面的 call / waitEvent：服务端的响应与事件
// 走同一条连接、先后不定（订阅回放的 session.synced 可能先于 subscribe 的响应到达），丢掉会让后面等不到
// （2026-11-07：CI 全量并行下 TestEventPush 偶发"没等到事件 session.synced"）。
func (c *client) take(what string, match func(map[string]json.RawMessage) bool) map[string]json.RawMessage {
	c.t.Helper()
	dl := time.After(5 * time.Second)
	var skipped []map[string]json.RawMessage
	defer func() { c.held = append(skipped, c.held...) }()
	for {
		m, ok := c.next(dl)
		if !ok {
			c.t.Fatalf("%s：5 秒内没等到", what)
		}
		if match(m) {
			return m
		}
		skipped = append(skipped, m)
	}
}

func (c *client) call(id int, cmd string, args any) map[string]json.RawMessage {
	c.t.Helper()
	b, _ := json.Marshal(map[string]any{"id": id, "cmd": cmd, "args": args})
	if err := websocket.Message.Send(c.ws, string(b)); err != nil {
		c.t.Fatal(err)
	}
	want, _ := json.Marshal(id)
	return c.take(cmd+" 的响应", func(m map[string]json.RawMessage) bool { return string(m["id"]) == string(want) })
}

func (c *client) waitEvent(kind string) wire.Envelope {
	c.t.Helper()
	var e wire.Envelope
	c.take("事件 "+kind, func(m map[string]json.RawMessage) bool {
		raw, has := m["event"]
		if !has {
			return false
		}
		var x wire.Envelope
		if json.Unmarshal(raw, &x) != nil || x.Kind != kind {
			return false
		}
		e = x
		return true
	})
	return e
}

func TestHelloRoundtrip(t *testing.T) {
	base, _ := newTestServer(t)
	c := dial(t, base)
	m := c.call(1, "hello", map[string]any{"client": "test", "protocol": map[string]int{"min": 2, "max": 2}})
	if string(m["ok"]) != "true" {
		t.Fatalf("hello：%s", m["error"])
	}
	var res wire.HelloResult
	if err := json.Unmarshal(m["result"], &res); err != nil || res.Protocol != wire.Protocol || res.Server != appapi.Server ||
		res.Principal != appapi.LocalPrincipal {
		t.Fatalf("hello 结果：%s %v", m["result"], err)
	}
	m = c.call(2, "hello", map[string]any{"client": "test", "protocol": map[string]int{"min": 1, "max": 1}})
	var e wire.Error
	_ = json.Unmarshal(m["error"], &e)
	if string(m["ok"]) != "false" || e.Code != wire.CodeUnsupported {
		t.Fatalf("协议区间不含 2 应为 unsupported：%v", m)
	}
	m = c.call(3, "no.such", nil)
	e = wire.Error{}
	_ = json.Unmarshal(m["error"], &e)
	if string(m["ok"]) != "false" || e.Code != wire.CodeUnsupported {
		t.Fatalf("未知命令：%v", m)
	}
	// 坏帧
	_ = websocket.Message.Send(c.ws, "not json")
	m = c.call(4, "sessions.list", map[string]any{})
	if string(m["ok"]) != "true" {
		t.Fatalf("sessions.list：%s", m["error"])
	}
}

// 事件只推连接订阅了的会话（10.5 事件过滤）：两个订阅了的连接都收到持久事件（带 cursor）与 turn.ended，
// 没订阅的连接收不到这个会话的事件。
func TestEventPush(t *testing.T) {
	base, _ := newTestServer(t)
	a, b, idle := dial(t, base), dial(t, base), dial(t, base)
	if m := a.call(1, "session.create", map[string]any{"session": "s1"}); string(m["ok"]) != "true" {
		t.Fatalf("create：%s", m["error"])
	}
	for i, c := range []*client{a, b} {
		if m := c.call(10+i, "session.subscribe", map[string]any{"session": "s1"}); string(m["ok"]) != "true" {
			t.Fatalf("subscribe：%s", m["error"])
		}
		c.waitEvent(wire.EvSynced)
	}
	if m := a.call(2, "session.send", map[string]any{"session": "s1", "text": "hi", "client_id": "c1"}); string(m["ok"]) != "true" {
		t.Fatalf("send：%s", m["error"])
	}
	for _, c := range []*client{a, b} {
		e := c.waitEvent(wire.EvEntryAppended)
		if e.Session != "s1" || e.Cursor == 0 {
			t.Fatalf("entry.appended：%+v", e)
		}
		c.waitEvent(wire.EvTurnEnded)
	}
	dl := time.After(200 * time.Millisecond)
	for {
		select {
		case m, ok := <-idle.ch:
			if !ok {
				return
			}
			if raw, has := m["event"]; has {
				var e wire.Envelope
				_ = json.Unmarshal(raw, &e)
				if e.Session == "s1" {
					t.Fatalf("没订阅的连接不应收到 s1 的事件：%+v", e)
				}
			}
		case <-dl:
			return
		}
	}
}

func TestToken(t *testing.T) {
	a, _ := NewToken()
	b, _ := NewToken()
	if len(a) != 43 || a == b || strings.ContainsAny(a, "+/=") {
		t.Fatalf("令牌：%q %q", a, b)
	}
}
