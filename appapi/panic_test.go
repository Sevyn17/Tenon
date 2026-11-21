package appapi

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/runtime/bus"
	"github.com/Sevyn17/tenon/runtime/host"
	"github.com/Sevyn17/tenon/runtime/store"
)

type panicListMCP struct{ fakeMCP }

func (panicListMCP) List(context.Context) (wire.MCPListResult, error) {
	panic("测试：MCP列表异常")
}

func TestConnCallPanicReturnsInternal(t *testing.T) {
	app := newAppOpts(t, func(o *Options) { o.MCP = &panicListMCP{} })
	c := app.NewConn(context.Background(), LocalPrincipal)
	defer c.Close()
	_, err := c.Call(context.Background(), "mcp.list", nil)
	wantCode(t, AsError(err), wire.CodeInternal, "")
	if _, err := c.Call(context.Background(), "hello", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("单条命令 panic不应破坏连接：%v", err)
	}
}

type panicJSON struct{}

func (panicJSON) MarshalJSON() ([]byte, error) {
	panic("测试：事件载荷异常")
}

func waitConnClosed(t *testing.T, c *Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		_, err := c.Next(ctx)
		if err == nil {
			continue
		}
		if !errors.Is(err, host.ErrClosed) {
			t.Fatalf("事件 panic后应关闭连接，得到：%v", err)
		}
		return
	}
}

func TestConnNoticeWatcherPanicClosesConnection(t *testing.T) {
	app := newApp(t)
	c := app.NewConn(context.Background(), LocalPrincipal)
	defer c.Close()
	app.Bus().Publish(bus.Envelope{Kind: EventNotice, Data: panicJSON{}})
	waitConnClosed(t, c)
}

func TestConnSessionWatcherPanicClosesConnection(t *testing.T) {
	app := newApp(t)
	if _, err := call2(t, app, "session.create", `{"session":"s"}`); err != nil {
		t.Fatal(err)
	}
	c := app.NewConn(context.Background(), LocalPrincipal)
	defer c.Close()
	app.Bus().Publish(bus.Envelope{Session: "s", Kind: store.EventRunState, Seq: 1, Data: panicJSON{}})
	waitConnClosed(t, c)
}

func TestConnSubscriptionPanicClosesConnection(t *testing.T) {
	app := newApp(t)
	if _, err := call2(t, app, "session.create", `{"session":"s"}`); err != nil {
		t.Fatal(err)
	}
	c := app.NewConn(context.Background(), LocalPrincipal)
	defer c.Close()
	if err := c.Subscribe(context.Background(), wire.SubscribeArgs{Session: "s"}); err != nil {
		t.Fatal(err)
	}
	waitKind(t, c, wire.EvSynced)
	app.Bus().Publish(bus.Envelope{Session: "s", Kind: EventTurnStart, Data: panicJSON{}})
	waitConnClosed(t, c)
}
