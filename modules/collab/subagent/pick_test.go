package subagent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Sevyn17/tenon/modules/storage/sqlite"
	"github.com/Sevyn17/tenon/runtime/agent"
	"github.com/Sevyn17/tenon/runtime/callctx"
	"github.com/Sevyn17/tenon/runtime/host"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

func ctxWith(use string) *callctx.Ctx {
	c := callctx.New(callctx.Options{SessionID: "p"})
	c.Bind(callctx.Session{ID: "p"}, nil, json.RawMessage(use))
	return c
}

// subagent 能选定义，但只能选父定义 [use.subagent] agents 列出的（2026-11-04 审查 P2：原先只能用通用定义）。
func TestPickDefinition(t *testing.T) {
	allow := ctxWith(`{"subagent":{"agents":["reviewer"]}}`)
	for _, c := range []struct {
		ctx  *callctx.Ctx
		name string
		want string
		err  string
	}{
		{allow, "", Definition, ""},
		{allow, "reviewer", "reviewer", ""},
		{allow, "admin", "", "不在可委派列表"},
		{ctxWith(`{}`), "reviewer", "", "没有列出可委派的智能体"},
		{nil, "reviewer", "", "没有列出可委派的智能体"},
	} {
		got, err := pickDefinition(&toolhub.Cx{Ctx: c.ctx}, c.name)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%q：应报 %q，得 %v", c.name, c.err, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%q：得 %q %v，期望 %q", c.name, got, err, c.want)
		}
	}
}

// 选中的定义名一路传到子会话的构造（会话行记 AgentID，恢复时还是同一个角色）。
func TestSubagentToolUsesChosenDefinition(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "s.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	var mu sync.Mutex
	var defs []string
	h := &host.Host{DB: db}
	h.New = func(id, def string) (*agent.Agent, *session.Config, error) {
		mu.Lock()
		defs = append(defs, def)
		mu.Unlock()
		hub := toolhub.New(toolhub.Options{Store: agent.ToolStore{DB: db}})
		return &agent.Agent{Store: db, SessionID: id, Model: echoModel{}, Hub: hub},
			&session.Config{Model: "m", Policy: session.DefaultPolicy()}, nil
	}
	tool := Tool(h, h.New)
	cx := &toolhub.Cx{SessionID: "p", CallID: "call-0000000000aa", Tool: tool, Ctx: ctxWith(`{"subagent":{"agents":["reviewer"]}}`)}
	out, err := tool.Call(ctx, cx, json.RawMessage(`{"task":"看看","agent":"reviewer"}`))
	if err != nil || out.Status.State != "ok" {
		t.Fatalf("委派失败：%v %+v", err, out)
	}
	if len(defs) != 1 || defs[0] != "reviewer" {
		t.Fatalf("子会话应按 reviewer 构造：%v", defs)
	}
	if out, _ := tool.Call(ctx, &toolhub.Cx{SessionID: "p", CallID: "call-0000000000bb", Tool: tool, Ctx: cx.Ctx},
		json.RawMessage(`{"task":"x","agent":"admin"}`)); out.Status.State != "error" {
		t.Fatalf("列表外的定义应拒绝：%+v", out)
	}
}
