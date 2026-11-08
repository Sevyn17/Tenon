package web

import (
	"encoding/json"
	"net"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"

	"golang.org/x/net/websocket"
)

func newIdleServer(t *testing.T, idle time.Duration) string {
	t.Helper()
	app := newTestApp(t)
	ts := httptest.NewUnstartedServer(nil)
	port := ts.Listener.Addr().(*net.TCPAddr).Port
	ts.Config.Handler = New(Config{API: app, Token: testToken, Hosts: LoopbackHosts(port), Idle: idle,
		Static: fstest.MapFS{"index.html": {Data: []byte("<html>ok</html>")}}})
	ts.Start()
	t.Cleanup(ts.Close)
	return ts.URL
}

// 穿插项 9：心跳帧 {"ping":n} 原样回 {"pong":n}，不当成命令（不回 bad_request）。
func TestHeartbeatPong(t *testing.T) {
	base, _ := newTestServer(t)
	c := dial(t, base)
	if err := websocket.Message.Send(c.ws, `{"ping":7}`); err != nil {
		t.Fatal(err)
	}
	m := c.take("pong", func(m map[string]json.RawMessage) bool { _, ok := m["pong"]; return ok })
	if string(m["pong"]) != "7" {
		t.Fatalf("pong 应原样带回 7，得 %s", m["pong"])
	}
	if _, bad := m["error"]; bad {
		t.Fatalf("心跳不应报错：%v", m)
	}
}

// 空闲超过 Idle 没收到任何帧，服务端关连接；期间一直发心跳则不关。
func TestIdleConnectionClosed(t *testing.T) {
	const idle = 400 * time.Millisecond
	base := newIdleServer(t, idle)

	alive := dial(t, base)
	stop := time.After(3 * idle)
	tick := time.NewTicker(idle / 4)
	defer tick.Stop()
beat:
	for {
		select {
		case <-tick.C:
			if err := websocket.Message.Send(alive.ws, `{"ping":1}`); err != nil {
				t.Fatalf("发心跳期间连接不应被关：%v", err)
			}
		case <-stop:
			break beat
		}
	}
	if m := alive.call(1, "hello", map[string]any{"client": "test", "protocol": map[string]int{"min": 2, "max": 2}}); string(m["ok"]) != "true" {
		t.Fatalf("心跳保活的连接应还能用：%v", m)
	}

	silent := dial(t, base)
	select {
	case _, ok := <-drain(silent):
		if ok {
			t.Fatal("不应收到帧")
		}
	case <-time.After(10 * idle):
		t.Fatalf("空闲 %v 后服务端应关闭连接", idle)
	}
}

// drain 跳过所有帧，连接关闭时返回的通道关闭。
func drain(c *client) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		for range c.ch {
		}
		close(done)
	}()
	return done
}
