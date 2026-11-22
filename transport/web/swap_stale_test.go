package web

// 换运行时（SwapAPI）之后、旧连接被关掉之前（约 200ms）客户端在旧连接上发的命令：旧 App 的存储已经关了，
// 不能再落到旧 App 上（2026-11-22 Mortise e2e「单独启停模型」报 sql: database is closed）。

import (
	"net"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/appapi"
)

func TestStaleConnAfterSwapUsesCurrentApp(t *testing.T) {
	var db1 interface{ Close() error }
	app1 := newTestApp(t, func(o *appapi.Options) { db1, _ = o.DB.(interface{ Close() error }) })
	app2 := newTestApp(t)
	if db1 == nil {
		t.Fatal("测试存储没有 Close")
	}
	ts := httptest.NewUnstartedServer(nil)
	port := ts.Listener.Addr().(*net.TCPAddr).Port
	srv := New(Config{API: app1, Token: testToken, Hosts: LoopbackHosts(port)})
	ts.Config.Handler = srv
	ts.Start()
	t.Cleanup(ts.Close)

	c := dial(t, ts.URL)
	if m := c.call(1, "hello", map[string]any{"client": "test", "protocol": map[string]int{"min": 2, "max": 2}}); string(m["ok"]) != "true" {
		t.Fatalf("hello：%s", m["error"])
	}
	// 同 apphost.WorkspaceManager.Reload：先关旧运行时（存储随之关闭），再换上新 App。
	_ = db1.Close()
	srv.SwapAPI(app2)
	// 旧连接还没断（SwapAPI 延后约 200ms 才关）：这时发的命令要落到新 App 上。
	m := c.call(2, "sessions.list", map[string]any{})
	if string(m["ok"]) != "true" {
		t.Fatalf("换运行时后旧连接上的命令应由新 App 执行：%s", m["error"])
	}
	if strings.Contains(string(m["error"]), "closed") {
		t.Fatalf("不应碰到已关闭的存储：%s", m["error"])
	}
}
