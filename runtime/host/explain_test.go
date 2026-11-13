package host

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/runtime/agent"
	"github.com/Sevyn17/tenon/runtime/mail"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
	"github.com/Sevyn17/tenon/runtime/store/memstore"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// newExplainRig 像运行时那样接上 toolhub → agent 的 span 观测，才能记录工具 trace 事件。
func newExplainRig(t *testing.T, m agent.Model) (*Host, store.Store) {
	t.Helper()
	db := memstore.New()
	t.Cleanup(func() { db.Close() })
	h := &Host{DB: db}
	h.New = func(id, def string) (*agent.Agent, *session.Config, error) {
		a := &agent.Agent{Store: db, SessionID: id, Model: m}
		hub := toolhub.New(toolhub.Options{Store: agent.ToolStore{DB: db}, OnSpan: a.HubSpan})
		echo := &toolhub.Tool{Spec: session.ToolSpec{Name: "echo", Schema: json.RawMessage(`{"type":"object"}`)},
			Risk: toolhub.Read, Call: func(ctx context.Context, _ *toolhub.Cx, args json.RawMessage) (toolhub.Output, error) {
				return toolhub.Text("echoed"), nil
			}}
		if err := hub.Register(echo); err != nil {
			return nil, nil, err
		}
		a.Hub = hub
		cfg := &session.Config{Model: "m", Tools: hub.Exposed(hub.SpecsFor(nil)), Policy: session.DefaultPolicy()}
		return a, cfg, nil
	}
	return h, db
}

// T3：explain <trace> 能列出一次任务经过的会话、信封和工具（08 8.7、09 9.10）。
func TestExplainTraceListsPath(t *testing.T) {
	h, db := newExplainRig(t, &scriptedModel{firstTool: true})
	hd := mustOpen(t, h, "s1")
	env := mail.Envelope{
		Trace: "tr1",
		From:  mail.Address{Kind: mail.AddrHuman, ID: "u1"},
		To:    mail.Address{Kind: mail.AddrSession, ID: "s1"},
		Kind:  mail.KindRequest,
		Body:  "hi",
	}
	// 链上已有 trace 的信封（发件箱派发）沿用 trace；入口 Deliver 会重新分配。
	if err := h.deliverQueued(context.Background(), env); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	out := traceText(t, db, "tr1")
	for _, want := range []string{"envelope", "session s1", "tool s1", "echo"} {
		if !strings.Contains(out, want) {
			t.Fatalf("explain 缺少 %q：\n%s", want, out)
		}
	}
}

// 信封回合之后的普通 Send 回合不沿用上一回合的 trace（否则工具事件会记到旧任务名下）。
func TestTraceNotCarriedIntoPlainTurn(t *testing.T) {
	h, _ := newExplainRig(t, &scriptedModel{})
	hd := mustOpen(t, h, "s1")
	if err := h.deliverQueued(context.Background(), mail.Envelope{Trace: "tr3",
		From: mail.Address{Kind: mail.AddrHuman, ID: "u1"}, To: mail.Address{Kind: mail.AddrSession, ID: "s1"},
		Kind: mail.KindRequest, Body: "hi"}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if got := hd.Agent().TraceID(); got != "tr3" {
		t.Fatalf("信封回合的 trace = %q，期望 tr3", got)
	}
	if err := hd.Do(Command{Kind: Send, ItemID: NewItemID(), Text: "第二轮"}); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if got := hd.Agent().TraceID(); got != "" {
		t.Fatalf("普通 Send 回合不应沿用旧 trace，得 %q", got)
	}
}

func TestExplainTraceGuardDenied(t *testing.T) {
	h, db := newExplainRig(t, &scriptedModel{})
	mustOpen(t, h, "s1")
	env := mail.Envelope{
		Trace: "tr2",
		Hop:   MaxHop,
		From:  mail.Address{Kind: mail.AddrHuman, ID: "u1"},
		To:    mail.Address{Kind: mail.AddrSession, ID: "s1"},
		Kind:  mail.KindRequest,
		Body:  "hi",
	}
	if err := h.deliverQueued(context.Background(), env); err == nil {
		t.Fatal("跳数超限应报错")
	}
	out := traceText(t, db, "tr2")
	if !strings.Contains(out, "guard_denied") || !strings.Contains(out, "hop") {
		t.Fatalf("explain 缺少拒绝原因：\n%s", out)
	}
}

// traceText 把一个 trace 的事件拼成「kind session detail」行（渲染在 appapi.RenderTrace，这里只看落库的事件）。
func traceText(t *testing.T, db store.TraceStore, trace string) string {
	t.Helper()
	evs, err := db.TraceEvents(context.Background(), trace)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, ev := range evs {
		b.WriteString(ev.Kind + " " + ev.Session + " " + ev.Detail + "\n")
	}
	return b.String()
}
