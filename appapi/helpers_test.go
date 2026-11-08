package appapi

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/runtime/agent"
	"github.com/Sevyn17/tenon/runtime/bus"
	"github.com/Sevyn17/tenon/runtime/host"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store/sqlite"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// fakeModel：最后一条是 user → 含 "tool" 调 echo，否则回 "re:<文本>"（经 onText 流式）；最后一条是 tool → 回 "done"。
type fakeModel struct{}

func (fakeModel) Stream(_ context.Context, req session.Request, onText func(string)) (session.AssistantItem, session.StopReason, error) {
	last := req.Messages[len(req.Messages)-1]
	txt := ""
	for _, p := range last.Content {
		txt += p.Text
	}
	if last.Role == "tool" {
		return session.AssistantItem{Content: session.Content{session.NewTextPart("done")}}, session.StopReason{V: "stop"}, nil
	}
	if strings.Contains(txt, "tool") {
		return session.AssistantItem{ToolCalls: []session.ToolCall{{Name: "echo", Args: json.RawMessage(`{"s":"x"}`)}}},
			session.StopReason{V: "tool_calls"}, nil
	}
	out := "re:" + txt
	if onText != nil {
		onText(out)
	}
	return session.AssistantItem{Content: session.Content{session.NewTextPart(out)}}, session.StopReason{V: "stop"}, nil
}

// askLayer 危险工具一律要确认（模拟权限插件）。
func askLayer() toolhub.Layer {
	return toolhub.Layer{Name: "permission", Guard: true, Call: func(ctx context.Context, cx *toolhub.Cx, args json.RawMessage, next toolhub.Next) (toolhub.Output, error) {
		if cx.Approved || (cx.Tool != nil && cx.Tool.Risk == toolhub.Read) {
			return next(ctx, cx, args)
		}
		return toolhub.Output{Kind: toolhub.Confirm, Reason: cx.Tool.Spec.Name + " 需要确认"}, nil
	}}
}

func newApp(t *testing.T) *App {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "s.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	b := bus.New(0)
	t.Cleanup(func() { b.Close(); db.Close() })
	var app *App
	h := &host.Host{DB: db, Bus: b}
	h.New = func(id, def string) (*agent.Agent, *session.Config, error) {
		hub := toolhub.New(toolhub.Options{Store: agent.ToolStore{DB: db}})
		echo := &toolhub.Tool{Spec: session.ToolSpec{Name: "echo", Schema: json.RawMessage(`{"type":"object"}`)},
			Risk: toolhub.Dangerous, Call: func(context.Context, *toolhub.Cx, json.RawMessage) (toolhub.Output, error) {
				return toolhub.Text("echoed"), nil
			}}
		if err := hub.Register(echo); err != nil {
			return nil, nil, err
		}
		hub.AddLayer(askLayer())
		a := &agent.Agent{Store: db, SessionID: id, Model: fakeModel{}, Hub: hub, Hooks: app.StreamHooks(id)}
		cfg := &session.Config{Model: "m", Tools: hub.Exposed(hub.SpecsFor(nil, 0)), Policy: session.DefaultPolicy()}
		return a, cfg, nil
	}
	app, err = New(Options{Host: h, Bus: b, DB: db, Model: "m", Workdir: "w"})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

// next 等到指定类型的事件。
func next(t *testing.T, s *bus.Sub, kind string) bus.Envelope {
	t.Helper()
	tm := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-s.C():
			if !ok {
				t.Fatalf("订阅已关闭（等 %s）：%v", kind, s.Err())
			}
			if ev.Kind == kind {
				return ev
			}
		case <-tm:
			t.Fatalf("超时：没等到 %s", kind)
		}
	}
}
