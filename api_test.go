package tenon_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Sevyn17/tenon"
	"github.com/Sevyn17/tenon/appapi"
	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/runtime/store/memstore"
	"github.com/Sevyn17/tenon/tenontest"
)

func protoCall[T any](t *testing.T, c *appapi.Conn, cmd string, args any) T {
	t.Helper()
	b, _ := json.Marshal(args)
	res, err := c.Call(context.Background(), cmd, b)
	if err != nil {
		t.Fatalf("%s：%v", cmd, err)
	}
	var out T
	raw, _ := json.Marshal(res)
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// SDK 装配的应用能直接提供应用协议：Start 之前 API() 为 nil；之后 hello 报出经 adapters 接上的能力
// （模型列表、权限档位、文件、追问、压缩），协议上建会话、发消息、等到回合结束。
func TestAPIServesProtocol(t *testing.T) {
	app, err := tenon.New(tenon.Default(), tenon.WithStore(memstore.New()), tenon.WithWorkdir(t.TempDir()),
		tenontest.Model(tenontest.Reply("你好")), tenon.WithProtocolUI())
	if err != nil {
		t.Fatal(err)
	}
	if app.API() != nil {
		t.Fatal("Start 之前 API() 应为 nil")
	}
	if err := app.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer app.Close(context.Background())
	api := app.API()
	if api == nil {
		t.Fatal("Start 之后 API() 不应为 nil")
	}
	c := api.NewConn(context.Background(), appapi.LocalPrincipal)
	defer c.Close()
	h := protoCall[wire.HelloResult](t, c, "hello", map[string]any{"protocol": map[string]int{"min": 2, "max": 2}})
	for _, want := range []string{"models", "modes", "files", "followups", "compact", "artifacts"} {
		if !slices.Contains(h.Capabilities, want) {
			t.Errorf("hello 应带 %s：%v", want, h.Capabilities)
		}
	}
	if h.Trusted {
		t.Error("没有 WithProjectTrust 时 hello.trusted 应为 false")
	}
	if ms := protoCall[wire.ModelsListResult](t, c, "models.list", map[string]any{}); len(ms.Models) == 0 {
		t.Error("models.list 应有模型")
	}
	snap := protoCall[wire.SessionSnapshot](t, c, "session.create", map[string]any{})
	if err := c.Subscribe(context.Background(), wire.SubscribeArgs{Session: snap.ID}); err != nil {
		t.Fatal(err)
	}
	protoCall[wire.SendResult](t, c, "session.send", map[string]any{"session": snap.ID, "text": "hi", "client_id": "c1"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		evs, err := c.Next(ctx)
		if err != nil {
			t.Fatalf("等回合结束：%v", err)
		}
		if slices.ContainsFunc(evs, func(e wire.Envelope) bool { return e.Kind == wire.EvTurnEnded }) {
			break
		}
	}
	if err := app.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if app.API() != nil {
		t.Fatal("Close 之后 API() 应为 nil")
	}
}

func TestProtocolUIConflictsWithUI(t *testing.T) {
	_, err := tenon.New(tenon.Default(), tenon.WithStore(memstore.New()), tenontest.Model(),
		tenon.WithProtocolUI(), tenon.WithUI(nopTestUI{}))
	if !errors.Is(err, tenon.ErrInvalid) {
		t.Fatalf("WithUI 与 WithProtocolUI 同时给应报 ErrInvalid：%v", err)
	}
}

type nopTestUI struct{}

func (nopTestUI) Print(string)        {}
func (nopTestUI) Warn(string, string) {}
func (nopTestUI) Ask(context.Context, string, []string) (string, error) {
	return "", errors.New("no")
}

// WithConfig 等同 profile 的 [plugin.<名>]：配置生效；组件不在装配里报 ErrInvalid。
func TestWithConfig(t *testing.T) {
	if _, err := tenon.New(tenon.Default(), tenon.WithStore(memstore.New()), tenontest.Model(),
		tenon.WithConfig("nope", map[string]any{"x": 1})); !errors.Is(err, tenon.ErrInvalid) {
		t.Fatalf("不在装配里的组件应报 ErrInvalid：%v", err)
	}
	app, err := tenon.New(tenon.Default(), tenon.WithStore(memstore.New()), tenon.WithWorkdir(t.TempDir()),
		tenon.WithConfig("models", map[string]any{"model": "deepseek-reasoner"}))
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer app.Close(context.Background())
	c := app.API().NewConn(context.Background(), appapi.LocalPrincipal)
	defer c.Close()
	if ms := protoCall[wire.ModelsListResult](t, c, "models.list", map[string]any{}); ms.Default != "deepseek-reasoner" {
		t.Fatalf("models.model 应生效：%q", ms.Default)
	}
}
