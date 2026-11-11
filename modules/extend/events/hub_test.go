package events

// T5b 单元：Hub.Post 把外部事件投给 agent:worker，worker 会话被建出并含正文；origin=external。

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/Sevyn17/tenon/runtime/agent"
	"github.com/Sevyn17/tenon/runtime/host"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store/sqlite"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

func rigHub(t *testing.T) (*Hub, *host.Host, *sqlite.DB) {
	t.Helper()
	db, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "e.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	h := &host.Host{DB: db}
	h.New = func(id, def string) (*agent.Agent, *session.Config, error) {
		hub := toolhub.New(toolhub.Options{Store: agent.ToolStore{DB: db}})
		a := &agent.Agent{Store: db, SessionID: id, Hub: hub}
		return a, &session.Config{Model: "m", Tools: hub.Exposed(hub.SpecsFor(nil, 0)), Policy: session.DefaultPolicy()}, nil
	}
	return &Hub{h: h}, h, db
}

func TestHubPostToAgent(t *testing.T) {
	hub, h, db := rigHub(t)
	ctx := context.Background()
	if err := hub.Post(ctx, "agent:worker", "事件正文"); err != nil {
		t.Fatal(err)
	}
	// worker 会话应被建出（agent:worker 不带 key）。
	infos, err := db.ListSessions(ctx, 0, 100, true)
	if err != nil || len(infos) == 0 {
		t.Fatalf("应有 worker 会话：%v %v", infos, err)
	}
	id := infos[0].ID
	if hd, ok := h.Get(id); ok {
		_ = hd.Wait()
	}
	hst, _ := db.Entries(ctx, id, 0, -1)
	found := false
	for _, e := range hst {
		if e.Item.User != nil {
			for _, p := range e.Item.User.Content {
				if p.Text != "" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatalf("worker 会话应含事件正文")
	}
}

// event_dispatch 工具把事件分发给 agent:worker（Router 分发的能力）。
func TestDispatchTool(t *testing.T) {
	hub, h, db := rigHub(t)
	ctx := context.Background()
	tool := dispatchTool(hub)
	out, err := tool.Call(ctx, &toolhub.Cx{SessionID: "router", CallID: "c1"}, json.RawMessage(`{"to":"agent:worker","body":"分发正文"}`))
	if err != nil || out.Status != session.ToolOK {
		t.Fatalf("dispatch 失败：%v %v", out, err)
	}
	infos, _ := db.ListSessions(ctx, 0, 100, true)
	if len(infos) == 0 {
		t.Fatal("dispatch 应建出 worker 会话")
	}
	if hd, ok := h.Get(infos[0].ID); ok {
		_ = hd.Wait()
	}
}
