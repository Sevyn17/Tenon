package subagent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"ybase-kernel/core/agent"
	"ybase-kernel/core/host"
	"ybase-kernel/core/session"
	"ybase-kernel/core/store/sqlite"
	"ybase-kernel/core/toolhub"
)

// echoModel 回 "re:<最后一条用户文本>"。
type echoModel struct{}

func (echoModel) Stream(ctx context.Context, req session.Request, onText func(string)) (session.AssistantItem, session.StopReason, error) {
	txt := ""
	for _, p := range req.Messages[len(req.Messages)-1].Content {
		txt += p.Text
	}
	return session.AssistantItem{Content: session.Content{session.NewTextPart("re:" + txt)}}, session.StopReason{V: "stop"}, nil
}

func TestSubagentTool(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "s.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	h := &host.Host{DB: db, New: func(id, def string) (*agent.Agent, *session.Config, error) {
		hub := toolhub.New(toolhub.Options{Store: agent.ToolStore{DB: db}})
		a := &agent.Agent{Store: db, SessionID: id, Model: echoModel{}, Hub: hub}
		return a, &session.Config{Model: "m", Policy: session.DefaultPolicy()}, nil
	}}
	tool := Tool(h, h.New)
	cx := &toolhub.Cx{SessionID: "p", CallID: "call-0123456789ab", Tool: tool}
	out, err := tool.Call(ctx, cx, json.RawMessage(`{"task":"做事"}`))
	if err != nil {
		t.Fatal(err)
	}
	if cx.ChildSessionID != "p/sub-456789ab" {
		t.Fatalf("子会话 id = %q", cx.ChildSessionID)
	}
	txt := ""
	for _, p := range out.Content {
		txt += p.Text
	}
	if !strings.Contains(txt, "re:做事") || !strings.Contains(txt, "p/sub-456789ab") {
		t.Fatalf("输出 = %q", txt)
	}
	if o, _ := tool.Call(ctx, cx, json.RawMessage(`{"task":" "}`)); o.Status.State == "" {
		t.Fatal("空 task 应报错")
	}
}
