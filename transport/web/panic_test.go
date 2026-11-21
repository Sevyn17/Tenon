package web

import (
	"context"
	"encoding/json"
	"net"
	"net/http/httptest"
	"testing"

	"github.com/Sevyn17/tenon/appapi"
	"github.com/Sevyn17/tenon/appapi/wire"
)

type panicWebMCP struct{}

func (panicWebMCP) List(context.Context) (wire.MCPListResult, error) {
	panic("测试：WebSocket命令异常")
}
func (panicWebMCP) Test(context.Context, wire.MCPServerDraft) ([]wire.MCPTool, error) {
	return nil, nil
}
func (panicWebMCP) SetEnabled(context.Context, string, bool, string) error { return nil }

func TestWSCommandPanicReturnsInternal(t *testing.T) {
	app := newTestApp(t, func(o *appapi.Options) { o.MCP = panicWebMCP{} })
	ts := httptest.NewUnstartedServer(nil)
	port := ts.Listener.Addr().(*net.TCPAddr).Port
	ts.Config.Handler = New(Config{API: app, Token: testToken, Hosts: LoopbackHosts(port)})
	ts.Start()
	t.Cleanup(ts.Close)
	c := dial(t, ts.URL)
	m := c.call(1, "mcp.list", nil)
	var e wire.Error
	if err := json.Unmarshal(m["error"], &e); err != nil || e.Code != wire.CodeInternal {
		t.Fatalf("命令 panic应返回 internal：%v err=%v", m, err)
	}
	m = c.call(2, "hello", map[string]any{"client": "test", "protocol": map[string]int{"min": 2, "max": 2}})
	if string(m["ok"]) != "true" {
		t.Fatalf("命令 panic不应断开连接：%v", m)
	}
}
