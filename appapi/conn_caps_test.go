package appapi

// 客户端改进 M5：传输层用 Conn.AddCapabilities 追加的能力并进 hello 的 capabilities（不重复、不影响别的连接）；
// App 自己不报 reconnect_event（CLI 等没有换运行时后的通知）。

import (
	"context"
	"slices"
	"testing"

	"github.com/Sevyn17/tenon/appapi/wire"
)

func connCaps(t *testing.T, c *Conn) []string {
	t.Helper()
	r, err := c.Call(context.Background(), "hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	return r.(wire.HelloResult).Capabilities
}

func TestConnAddCapabilities(t *testing.T) {
	app := newApp(t)
	if slices.Contains(capsOf(t, app), CapReconnectEvent) {
		t.Fatalf("App 自己不该报 %s", CapReconnectEvent)
	}
	plain := app.NewConn(context.Background(), LocalPrincipal)
	defer plain.Close()
	c := app.NewConn(context.Background(), LocalPrincipal)
	defer c.Close()
	c.AddCapabilities(CapReconnectEvent, CapReconnectEvent, "last_end") // 重复的、App 已有的都不重复报

	got := connCaps(t, c)
	if n := countOf(got, CapReconnectEvent); n != 1 {
		t.Fatalf("%s 应出现一次，实际 %d：%v", CapReconnectEvent, n, got)
	}
	if n := countOf(got, "last_end"); n != 1 {
		t.Fatalf("last_end 应出现一次，实际 %d：%v", n, got)
	}
	if slices.Contains(connCaps(t, plain), CapReconnectEvent) {
		t.Fatalf("别的连接不该带上 %s", CapReconnectEvent)
	}
	if slices.Contains(capsOf(t, app), CapReconnectEvent) {
		t.Fatalf("连接的能力不该写回 App 的能力表")
	}
}

func countOf(xs []string, x string) int {
	n := 0
	for _, v := range xs {
		if v == x {
			n++
		}
	}
	return n
}
