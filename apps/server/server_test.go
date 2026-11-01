package main

// 网页服务测试：令牌、Host / Origin 检查、hello 往返、事件推送、多连接独立订阅、监听地址规则。

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"golang.org/x/net/websocket"

	"ybase-kernel/core/agent"
	"ybase-kernel/core/appapi"
	"ybase-kernel/core/bus"
	"ybase-kernel/core/host"
	"ybase-kernel/core/session"
	"ybase-kernel/core/store/sqlite"
	"ybase-kernel/core/toolhub"
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
	h.New = func(id string) (*agent.Agent, *session.Config, error) {
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
	t  *testing.T
	ws *websocket.Conn
	ch chan map[string]json.RawMessage
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

func (c *client) call(id int, cmd string, args any) map[string]json.RawMessage {
	c.t.Helper()
	b, _ := json.Marshal(map[string]any{"id": id, "cmd": cmd, "args": args})
	if err := websocket.Message.Send(c.ws, string(b)); err != nil {
		c.t.Fatal(err)
	}
	want, _ := json.Marshal(id)
	dl := time.After(5 * time.Second)
	for {
		select {
		case m, ok := <-c.ch:
			if !ok {
				c.t.Fatal("连接已关")
			}
			if string(m["id"]) == string(want) {
				return m
			}
		case <-dl:
			c.t.Fatalf("%s 无响应", cmd)
		}
	}
}

func (c *client) waitEvent(kind string) bus.Envelope {
	c.t.Helper()
	dl := time.After(5 * time.Second)
	for {
		select {
		case m, ok := <-c.ch:
			if !ok {
				c.t.Fatal("连接已关")
			}
			if raw, has := m["event"]; has {
				var e bus.Envelope
				_ = json.Unmarshal(raw, &e)
				if e.Kind == kind {
					return e
				}
			}
		case <-dl:
			c.t.Fatalf("没等到事件 %s", kind)
		}
	}
}

func TestHelloRoundtrip(t *testing.T) {
	base, _ := newTestServer(t)
	c := dial(t, base)
	m := c.call(1, "hello", map[string]any{"client": "test", "version": 1})
	if string(m["ok"]) != "true" {
		t.Fatalf("hello：%s", m["error"])
	}
	var res appapi.HelloResult
	if err := json.Unmarshal(m["result"], &res); err != nil || res.Protocol != appapi.Protocol || res.Server != appapi.Server {
		t.Fatalf("hello 结果：%s %v", m["result"], err)
	}
	m = c.call(2, "no.such", nil)
	var e appapi.Error
	_ = json.Unmarshal(m["error"], &e)
	if string(m["ok"]) != "false" || e.Code != appapi.CodeUnsupported {
		t.Fatalf("未知命令：%v", m)
	}
	// 坏帧
	_ = websocket.Message.Send(c.ws, "not json")
	m = c.call(3, "sessions.list", map[string]any{})
	if string(m["ok"]) != "true" {
		t.Fatalf("sessions.list：%s", m["error"])
	}
}

func TestEventPush(t *testing.T) {
	base, _ := newTestServer(t)
	a, b := dial(t, base), dial(t, base) // 两个连接各自订阅
	if m := a.call(1, "session.open", map[string]any{"session": "s1"}); string(m["ok"]) != "true" {
		t.Fatalf("open：%s", m["error"])
	}
	if m := a.call(2, "session.send", map[string]any{"session": "s1", "text": "hi"}); string(m["ok"]) != "true" {
		t.Fatalf("send：%s", m["error"])
	}
	for _, c := range []*client{a, b} {
		e := c.waitEvent("entry")
		if e.Session != "s1" || e.Seq == 0 {
			t.Fatalf("entry：%+v", e)
		}
		c.waitEvent(appapi.EventTurnEnd)
	}
}

func TestCheckAddr(t *testing.T) {
	cases := []struct {
		o    options
		ok   bool
		loop bool
	}{
		{options{addr: "127.0.0.1:0"}, true, true},
		{options{addr: "localhost:8080"}, true, true},
		{options{addr: "[::1]:0"}, true, true},
		{options{addr: "0.0.0.0:80"}, false, false},
		{options{addr: ":80"}, false, false},
		{options{addr: "0.0.0.0:80", allowRemote: true}, false, false},
		{options{addr: "0.0.0.0:80", allowRemote: true, cert: "c", key: "k"}, true, false},
		{options{addr: "bad"}, false, false},
	}
	for _, c := range cases {
		loop, err := checkAddr(c.o)
		if (err == nil) != c.ok || loop != c.loop {
			t.Errorf("%+v：loop=%v err=%v", c.o, loop, err)
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
