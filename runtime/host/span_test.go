package host

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Sevyn17/tenon/runtime/agent"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store/memstore"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// M4.7：span 层级 turn → step → attempt / tool。
// 存储实现已迁出框架，这里只断言宿主写进存储的 span，不查表。
func TestSpanHierarchy(t *testing.T) {
	ctx := context.Background()
	db := memstore.New()
	defer db.Close()
	h := &Host{DB: db, New: func(id, def string) (*agent.Agent, *session.Config, error) {
		a := &agent.Agent{Store: db, SessionID: id, Model: &fakeModel{}}
		hub := toolhub.New(toolhub.Options{Store: agent.ToolStore{DB: db}, OnSpan: a.HubSpan})
		if err := hub.Register(&toolhub.Tool{Spec: session.ToolSpec{Name: "echo", Schema: json.RawMessage(`{"type":"object"}`)},
			Risk: toolhub.Dangerous, Call: func(context.Context, *toolhub.Cx, json.RawMessage) (toolhub.Output, error) {
				return toolhub.Text("echoed"), nil
			}}); err != nil {
			return nil, nil, err
		}
		a.Hub = hub
		return a, &session.Config{Model: "m", Tools: hub.Exposed(hub.SpecsFor(nil)), Policy: session.DefaultPolicy()}, nil
	}}
	hd, err := h.Open(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	if err := hd.Do(Command{Kind: Send, Text: "use tool"}); err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	kind := map[string]string{}
	parent := map[string]string{}
	count := map[string]int{}
	for _, sp := range db.SpansForTest("s") {
		kind[sp.ID], parent[sp.ID] = sp.Kind, sp.Parent
		count[sp.Kind]++
	}
	if count["turn"] != 1 || count["step"] != 2 || count["attempt"] != 2 || count["tool"] != 1 {
		t.Fatalf("span 数量不符: %v", count)
	}
	want := map[string]string{"turn": "", "step": "turn", "attempt": "step", "tool": "step"}
	for id, k := range kind {
		pk := ""
		if parent[id] != "" {
			pk = kind[parent[id]]
		}
		if pk != want[k] {
			t.Fatalf("%s span 的父应为 %q，得 %q（parent=%q）", k, want[k], pk, parent[id])
		}
	}
}
