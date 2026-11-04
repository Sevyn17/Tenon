package host

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"ybase/runtime/agent"
	"ybase/runtime/mail"
	"ybase/runtime/session"
	"ybase/runtime/store/sqlite"
	"ybase/runtime/toolhub"
)

// newExplainRig 像运行时那样接上 toolhub → agent 的 span 观测，才能记录工具 trace 事件。
func newExplainRig(t *testing.T, m agent.Model) (*Host, *sqlite.DB) {
	t.Helper()
	db, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
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
		cfg := &session.Config{Model: "m", Tools: hub.Exposed(hub.SpecsFor(nil, 0)), Policy: session.DefaultPolicy()}
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
	out, err := ExplainTrace(context.Background(), db, "tr1")
	if err != nil {
		t.Fatalf("ExplainTrace: %v", err)
	}
	for _, want := range []string{"trace tr1", "会话：s1", "信封", "会话", "工具", "echo"} {
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
	out, err := ExplainTrace(context.Background(), db, "tr2")
	if err != nil {
		t.Fatalf("ExplainTrace: %v", err)
	}
	if !strings.Contains(out, "拒绝") || !strings.Contains(out, "hop") {
		t.Fatalf("explain 缺少拒绝原因：\n%s", out)
	}
}
