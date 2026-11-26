package appapi

// 回合结束原因进持久状态（客户端改进 M2 / C-1、N-5）：模型立即失败时，新连接的 session.get、
// 从 0 回放的 session.state_changed 都能拿到 last_end，失败码与说明分开；drive 返回 error 时也有 turn.ended。

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/runtime/agent"
	"github.com/Sevyn17/tenon/runtime/bus"
	"github.com/Sevyn17/tenon/runtime/host"
	"github.com/Sevyn17/tenon/runtime/modelhub"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store/memstore"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// noCredModel 模拟没配凭证：每次调用都返回 MISSING_CREDENTIAL（与 modelhub 解析提供方失败时同形）。
type noCredModel struct{}

func (noCredModel) Stream(context.Context, session.Request, func(string)) (session.AssistantItem, session.StopReason, error) {
	return session.AssistantItem{}, session.StopReason{}, &modelhub.Failure{Code: modelhub.CodeMissingCred,
		Message: "提供方 deepseek 凭证 env:DEEPSEEK_API_KEY 不可用"}
}

// newAppWith 同 newApp，模型可换；withHub = false 时智能体没有工具管理器（模型一调工具 drive 就返回 error）。
func newAppWith(t *testing.T, m agent.Model, withHub bool) *App {
	t.Helper()
	db := memstore.New()
	b := bus.New(0)
	var app *App
	h := &host.Host{Workspace: "w", DB: db, Bus: b}
	t.Cleanup(func() { h.Close(); b.Close(); db.Close() })
	echo := session.ToolSpec{Name: "echo", Schema: json.RawMessage(`{"type":"object"}`)}
	h.New = func(id, def string) (*agent.Agent, *session.Config, error) {
		a := &agent.Agent{Store: db, SessionID: id, Model: m, Hooks: app.StreamHooks(id)}
		if withHub {
			a.Hub = toolhub.New(toolhub.Options{Store: agent.ToolStore{DB: db}})
		}
		return a, &session.Config{Model: "m", Tools: []session.ToolSpec{echo}, Policy: session.DefaultPolicy()}, nil
	}
	app, err := New(Options{Host: h, Bus: b, DB: db, Model: "m", Workdir: "w"})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

// subscribeLive 新连接从 0 订阅并等到 synced，之后的事件是实时的。
func subscribeLive(t *testing.T, app *App, id string) *Conn {
	t.Helper()
	c := app.NewConn(context.Background(), LocalPrincipal)
	t.Cleanup(c.Close)
	zero := int64(0)
	if err := c.Subscribe(context.Background(), wire.SubscribeArgs{Session: id, After: &zero}); err != nil {
		t.Fatal(err)
	}
	waitKind(t, c, wire.EvSynced)
	return c
}

// sendTurnEnded 发一条消息，等实时的 turn.ended。turn.ended 在结束原因提交之后才发，收到后再读快照一定读得到。
func sendTurnEnded(t *testing.T, app *App, c *Conn, id, text string) wire.TurnEnded {
	t.Helper()
	if _, e := call2(t, app, "session.send", `{"session":"`+id+`","text":"`+text+`","client_id":"c1"}`); e != nil {
		t.Fatal(e)
	}
	evs := waitKind(t, c, wire.EvTurnEnded)
	return evs[len(evs)-1].Data.(wire.TurnEnded)
}

// freshSnapshot 用一个新连接取快照（模拟刷新页面）。
func freshSnapshot(t *testing.T, app *App, id string) wire.SessionSnapshot {
	t.Helper()
	c := app.NewConn(context.Background(), LocalPrincipal)
	defer c.Close()
	out, err := c.Call(context.Background(), "session.get", json.RawMessage(`{"session":"`+id+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	return out.(wire.SessionSnapshot)
}

// replayedLastEnd 新连接从 0 回放，返回最后一条带 last_end 的 session.state_changed 的 last_end。
func replayedLastEnd(t *testing.T, app *App, id string) *wire.TurnEnded {
	t.Helper()
	c := app.NewConn(context.Background(), LocalPrincipal)
	defer c.Close()
	zero := int64(0)
	if err := c.Subscribe(context.Background(), wire.SubscribeArgs{Session: id, After: &zero}); err != nil {
		t.Fatal(err)
	}
	var last *wire.TurnEnded
	for _, e := range waitKind(t, c, wire.EvSynced) {
		if sc, ok := e.Data.(wire.StateChanged); ok && e.Kind == wire.EvStateChanged && sc.LastEnd != nil {
			if sc.RunState != wire.RunIdle {
				t.Fatalf("带 last_end 的 state_changed 应为 idle：%+v", sc)
			}
			last = sc.LastEnd
		}
	}
	return last
}

// 首页「先发送后订阅」：模型立即失败（无凭证），刷新后的新连接从快照与回放都能补回失败原因，码与说明分开。
func TestModelFailureLastEndSurvivesReconnect(t *testing.T) {
	app := newAppWith(t, noCredModel{}, true)
	if _, e := call2(t, app, "session.create", `{"session":"s"}`); e != nil {
		t.Fatal(e)
	}
	c := subscribeLive(t, app, "s")
	te := sendTurnEnded(t, app, c, "s", "你好")
	const msg = "提供方 deepseek 凭证 env:DEEPSEEK_API_KEY 不可用"
	if te.Kind != "failed" || te.Code != "MISSING_CREDENTIAL" || te.Message != msg || te.Turn != 1 || te.At == 0 {
		t.Fatalf("turn.ended 应为 failed，code 只放错误码、message 放说明、带 at：%+v", te)
	}

	snap := freshSnapshot(t, app, "s")
	le := snap.LastEnd
	if snap.RunState != wire.RunIdle || le == nil || le.Kind != "failed" || le.Code != "MISSING_CREDENTIAL" ||
		le.Message != msg || le.Turn != 1 || le.At == 0 {
		t.Fatalf("新连接 session.get 应带 last_end（failed，码与说明分开）：run=%s %+v", snap.RunState, le)
	}
	if strings.Contains(le.Code, ":") {
		t.Fatalf("code 不应再拼上说明：%q", le.Code)
	}
	if r := replayedLastEnd(t, app, "s"); r == nil || *r != *le {
		t.Fatalf("从 0 回放的 state_changed 应带同一个 last_end：%+v，快照 %+v", r, le)
	}

	h, e := call2(t, app, "hello", `{}`)
	if e != nil {
		t.Fatal(e)
	}
	if !slices.Contains(h.(wire.HelloResult).Capabilities, "last_end") {
		t.Fatalf("hello 应登记 last_end 能力：%v", h.(wire.HelloResult).Capabilities)
	}
}

// drive 返回 error（模型调了工具、智能体却没有工具管理器）：原先只发内部的 turn.failed，客户端只看到 idle；
// 现在补发 turn.ended（兜底码 TURN_FAILED，说明是原始错误），快照里也有。
func TestDriveErrorSendsTurnEnded(t *testing.T) {
	app := newAppWith(t, fakeModel{}, false)
	if _, e := call2(t, app, "session.create", `{"session":"s"}`); e != nil {
		t.Fatal(e)
	}
	c := subscribeLive(t, app, "s")
	te := sendTurnEnded(t, app, c, "s", "调 tool")
	if te.Kind != "failed" || te.Code != agent.CodeTurnFailed || !strings.Contains(te.Message, "工具管理器") || te.Turn != 1 {
		t.Fatalf("drive 返回 error 时也应有 turn.ended：%+v", te)
	}
	le := freshSnapshot(t, app, "s").LastEnd
	if le == nil || le.Kind != "failed" || le.Code != agent.CodeTurnFailed || le.Message != te.Message {
		t.Fatalf("快照的 last_end 应与 turn.ended 一致：%+v", le)
	}
}

// 正常完成的回合也记 last_end（kind = done）：前端据此清掉上一回合的错误卡。
func TestDoneTurnLastEnd(t *testing.T) {
	app := newApp(t)
	if _, e := call2(t, app, "session.create", `{"session":"s"}`); e != nil {
		t.Fatal(e)
	}
	if le := freshSnapshot(t, app, "s").LastEnd; le != nil {
		t.Fatalf("还没跑过回合的会话不应有 last_end：%+v", le)
	}
	sendWait(t, app, "s", "a", "c1")
	if le := freshSnapshot(t, app, "s").LastEnd; le == nil || le.Kind != "done" || le.Turn != 1 || le.Code != "" {
		t.Fatalf("完成的回合 last_end 应为 done：%+v", le)
	}
	if r := replayedLastEnd(t, app, "s"); r == nil || r.Kind != "done" {
		t.Fatalf("回放应带 done 的 last_end：%+v", r)
	}
}
