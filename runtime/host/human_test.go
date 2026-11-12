package host

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/modules/storage/sqlite"
	"github.com/Sevyn17/tenon/runtime/agent"
	"github.com/Sevyn17/tenon/runtime/bus"
	"github.com/Sevyn17/tenon/runtime/mail"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// newHumanRig 用会触发确认的工具（Dangerous）搭一个宿主。
func newHumanRig(t *testing.T, m agent.Model) (*Host, *sqlite.DB) {
	t.Helper()
	db, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	h := &Host{DB: db, Bus: bus.New(0)}
	h.New = func(id, def string) (*agent.Agent, *session.Config, error) {
		a := &agent.Agent{Store: db, SessionID: id, Model: m}
		hub := toolhub.New(toolhub.Options{Store: agent.ToolStore{DB: db}, OnSpan: a.HubSpan})
		echo := &toolhub.Tool{Spec: session.ToolSpec{Name: "echo", Schema: json.RawMessage(`{"type":"object"}`)},
			Risk: toolhub.Dangerous, Call: func(ctx context.Context, _ *toolhub.Cx, args json.RawMessage) (toolhub.Output, error) {
				return toolhub.Text("echoed"), nil
			}}
		hub.AddGate(askGate()) // 非只读工具先要求确认（D3）
		if err := hub.Register(echo); err != nil {
			return nil, nil, err
		}
		a.Hub = hub
		cfg := &session.Config{Model: "m", Tools: hub.Exposed(hub.SpecsFor(nil, 0)), Policy: session.DefaultPolicy()}
		return a, cfg, nil
	}
	return h, db
}

// 11 11.2：工具确认经 human: 地址——内核签发 corr、发布 human_requested，
// 回信信封（KindReply + corr）投回会话后工具继续，不当作普通消息入信箱。
func TestHumanConfirmViaEnvelope(t *testing.T) {
	h, _ := newHumanRig(t, &scriptedModel{firstTool: true})
	sub := h.Bus.Subscribe(context.Background(), bus.Filter{Kinds: []string{"human_requested"}})
	hd := mustOpen(t, h, "s1")
	if err := hd.Do(Command{Kind: Send, ItemID: NewItemID(), Text: "跑工具"}); err != nil {
		t.Fatalf("Do: %v", err)
	}
	var corr string
	select {
	case ev := <-sub.C():
		if data, ok := ev.Data.(map[string]any); ok {
			corr, _ = data["corr"].(string)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("没收到 human_requested")
	}
	if corr == "" {
		t.Fatal("human_requested 里没有 corr")
	}
	reply := mail.Envelope{
		From: mail.Address{Kind: mail.AddrHuman, ID: "u1"},
		To:   mail.Address{Kind: mail.AddrSession, ID: "s1"},
		Kind: mail.KindReply, Corr: corr, Body: "approve",
	}
	if err := h.Deliver(context.Background(), reply); err != nil {
		t.Fatalf("Deliver 回信: %v", err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	var joined string
	for _, m := range messages(t, hd) {
		joined += textOfMsg(t, m)
	}
	if !strings.Contains(joined, "echoed") {
		t.Fatalf("确认后工具应执行，历史：\n%s", joined)
	}
}

// 回信被拒（corr 不匹配）时工具拿到拒绝；同一 corr 不能重放。
func TestHumanConfirmDeniedAndReplay(t *testing.T) {
	h, _ := newHumanRig(t, &scriptedModel{firstTool: true})
	sub := h.Bus.Subscribe(context.Background(), bus.Filter{Kinds: []string{"human_requested"}})
	hd := mustOpen(t, h, "s1")
	_ = hd.Do(Command{Kind: Send, ItemID: NewItemID(), Text: "跑工具"})
	var corr string
	select {
	case ev := <-sub.C():
		data, _ := ev.Data.(map[string]any)
		corr, _ = data["corr"].(string)
	case <-time.After(5 * time.Second):
		t.Fatal("没收到 human_requested")
	}
	reply := mail.Envelope{From: mail.Address{Kind: mail.AddrHuman, ID: "u1"},
		To: mail.Address{Kind: mail.AddrSession, ID: "s1"}, Kind: mail.KindReply, Corr: corr, Body: "no"}
	if err := h.Deliver(context.Background(), reply); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if err := h.Deliver(context.Background(), reply); err == nil {
		t.Fatal("重放同一 corr 应被拒")
	}
}

func TestDecisionFromBody(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"approve", true}, {"ALLOW", true}, {"YES", false}, {"同意", false}, {"no", false},
		{`{"approve":true}`, true}, {`{"approve":false}`, false}, {`{"decision":"allow"}`, true},
	} {
		if got := decisionFromBody(tc.in); got != tc.want {
			t.Fatalf("decisionFromBody(%q)=%v，期望 %v", tc.in, got, tc.want)
		}
	}
}
