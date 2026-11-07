package host

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"tenon/runtime/agent"
	"tenon/runtime/session"
	"tenon/runtime/store/sqlite"
	"tenon/runtime/toolhub"
)

// M4.7：span 层级 turn → step → attempt / tool，全部落 trace_spans。
func TestSpanHierarchy(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "s.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
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
		return a, &session.Config{Model: "m", Tools: hub.Exposed(hub.SpecsFor(nil, 0)), Policy: session.DefaultPolicy()}, nil
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
	rows, err := db.DB().QueryContext(ctx, `SELECT span_id, parent_span, kind FROM trace_spans WHERE session_id = ?`, "s")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	kind := map[string]string{}
	parent := map[string]string{}
	count := map[string]int{}
	for rows.Next() {
		var id, p, k string
		if err := rows.Scan(&id, &p, &k); err != nil {
			t.Fatal(err)
		}
		kind[id], parent[id] = k, p
		count[k]++
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
