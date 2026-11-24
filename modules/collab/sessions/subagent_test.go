package sessions

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/runtime/agent"
	"github.com/Sevyn17/tenon/runtime/host"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store/memstore"
	"github.com/Sevyn17/tenon/runtime/toolhub"
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
	db := memstore.New()
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
	if o, _ := tool.Call(ctx, cx, json.RawMessage(`{"task":" "}`)); o.Status != session.ToolError {
		t.Fatal("空 task 应报错")
	}
	if tool.Spec.Name != "session_start" {
		t.Fatalf("工具名 = %q", tool.Spec.Name)
	}
	// 子会话 origin 字面量不变（docs/73 R1：宿主按它判断子会话）。
	if l, err := db.Load(ctx, "p/sub-456789ab", nil); err != nil || l.Meta.Origin != "subagent" || l.Meta.ParentID != "p" {
		t.Fatalf("子会话行：%+v %v", l.Meta, err)
	}
	// 显式 child / wait 照常可用。
	cx2 := &toolhub.Cx{SessionID: "p", CallID: "call-0123456789cd", Tool: tool}
	if o, err := tool.Call(ctx, cx2, json.RawMessage(`{"task":"再做","relation":"child","reply":"wait"}`)); err != nil || o.Status != session.ToolOK {
		t.Fatalf("child/wait 应成功：%+v %v", o.Status, err)
	}
}

// relation=independent、reply=notify|none 本版未实现：返回失败，不建会话（docs/73 §2.1）。
func TestSessionStartRejectsUnimplemented(t *testing.T) {
	ctx := context.Background()
	db := memstore.New()
	t.Cleanup(func() { db.Close() })
	h := &host.Host{DB: db, New: func(id, def string) (*agent.Agent, *session.Config, error) {
		hub := toolhub.New(toolhub.Options{Store: agent.ToolStore{DB: db}})
		return &agent.Agent{Store: db, SessionID: id, Model: echoModel{}, Hub: hub}, &session.Config{Model: "m", Policy: session.DefaultPolicy()}, nil
	}}
	tool := Tool(h, h.New)
	for _, args := range []string{
		`{"task":"x","relation":"independent"}`,
		`{"task":"x","reply":"notify"}`,
		`{"task":"x","reply":"none"}`,
		`{"task":"x","relation":"bogus"}`,
	} {
		cx := &toolhub.Cx{SessionID: "p", CallID: "call-00000000ffff", Tool: tool}
		o, err := tool.Call(ctx, cx, json.RawMessage(args))
		if err != nil || o.Status != session.ToolError {
			t.Fatalf("%s 应失败：%+v %v", args, o.Status, err)
		}
		if cx.ChildSessionID != "" {
			t.Fatalf("%s 不应建子会话", args)
		}
	}
	if ok, _ := db.Exists(ctx, "p/sub-0000ffff"); ok {
		t.Fatal("拒绝时不应建子会话")
	}
}
