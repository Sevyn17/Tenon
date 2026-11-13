package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
	"github.com/Sevyn17/tenon/runtime/store/memstore"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// fakeModel：最后一条是工具结果时回一句文本，否则让模型调 echo 工具。
type fakeModel struct{ fail bool }

func (m fakeModel) Stream(_ context.Context, req session.Request, onText func(string)) (session.AssistantItem, session.StopReason, error) {
	if m.fail {
		return session.AssistantItem{}, session.StopReason{}, errors.New("模型服务 500")
	}
	last := req.Messages[len(req.Messages)-1]
	if last.Role == "tool" {
		s := "工具说：" + contentText(last.Content)
		if onText != nil {
			onText(s)
		}
		return session.AssistantItem{Content: session.Content{session.NewTextPart(s)}}, session.StopReason{V: "EndTurn"}, nil
	}
	return session.AssistantItem{ToolCalls: []session.ToolCall{{WireID: "call_1", Name: "echo", Args: json.RawMessage(`{"s":"pong"}`)}}},
		session.StopReason{V: "ToolUse"}, nil
}

// echoTool：一个有副作用的 echo 工具；runs 非空时记录执行次数。
func echoTool(runs *int) *toolhub.Tool {
	return &toolhub.Tool{Spec: echoSpec, Risk: toolhub.Write,
		Call: func(_ context.Context, _ *toolhub.Cx, raw json.RawMessage) (toolhub.Output, error) {
			if runs != nil {
				*runs++
			}
			var a struct{ S string }
			_ = json.Unmarshal(raw, &a)
			return toolhub.Text(a.S), nil
		}}
}

var echoSpec = session.ToolSpec{Name: "echo", Schema: json.RawMessage(`{"type":"object"}`)}

func testCfg() *session.Config {
	return &session.Config{Model: "m", Tools: []session.ToolSpec{echoSpec}, Policy: session.DefaultPolicy()}
}

func openStore(t *testing.T) store.Store {
	t.Helper()
	db := memstore.New()
	t.Cleanup(func() { db.Close() })
	return db
}

// crashStore 在工具执行前后注入崩溃（tool_started：写 started 后；tool_ran：执行后、写 done 前）。
type crashStore struct {
	ToolStore
	at string
}

func (s crashStore) PutToolStarted(ctx context.Context, sid, cid, h string) error {
	if err := s.ToolStore.PutToolStarted(ctx, sid, cid, h); err != nil {
		return err
	}
	if s.at == "tool_started" {
		return errCrash
	}
	return nil
}

func (s crashStore) FinishTool(ctx context.Context, sid, cid, from, out string) error {
	if s.at == "tool_ran" {
		return errCrash
	}
	return s.ToolStore.FinishTool(ctx, sid, cid, from, out)
}

type agentOpts struct {
	runs  *int
	ask   Ask    // 非空：加“每次询问” Guard，确认走 ask
	crash string // 故障注入点
	model Model
}

func newAgent(t *testing.T, db store.Store, o agentOpts) *Agent {
	t.Helper()
	m := o.model
	if m == nil {
		m = fakeModel{}
	}
	a := &Agent{Model: m, Store: db, SessionID: "s1", crashAt: o.crash}
	opts := toolhub.Options{Store: crashStore{ToolStore{DB: db}, o.crash}, OnSpan: a.HubSpan}
	if o.ask != nil {
		opts.Await = a.AwaitFunc(o.ask)
	}
	a.Hub = toolhub.New(opts)
	if err := a.Hub.Register(echoTool(o.runs)); err != nil {
		t.Fatal(err)
	}
	if o.ask != nil {
		a.Hub.AddGate(askGate())
	}
	if _, err := a.Open(context.Background(), testCfg()); err != nil {
		t.Fatal(err)
	}
	return a
}

func tempStore(t *testing.T) store.Store { return openStore(t) }

func TestToolLoopEndToEnd(t *testing.T) {
	db := tempStore(t)
	approvals := 0
	a := newAgent(t, db, agentOpts{ask: func(context.Context, *toolhub.Cx, string, time.Time) bool { approvals++; return true }})
	var text strings.Builder
	a.Hooks.OnText = func(d string) { text.WriteString(d) }

	reason, err := a.Send(context.Background(), "ping")
	if err != nil {
		t.Fatal(err)
	}
	if reason.Kind != session.EndDone || text.String() != "工具说：pong" || approvals != 1 {
		t.Fatalf("reason=%s text=%q approvals=%d", reason, text.String(), approvals)
	}

	// 重新打开：历史为 user / assistant(call) / tool / assistant，且无未完成回合。
	a2 := newAgent(t, db, agentOpts{})
	if a2.Pending() {
		t.Fatal("正常结束的会话不应有待续回合")
	}
	h := History(a2.Core)
	roles := make([]string, len(h))
	for i, m := range h {
		roles[i] = m.Role
	}
	if strings.Join(roles, ",") != "user,assistant,tool,assistant" {
		t.Fatalf("roles=%v", roles)
	}
	if reason, err := a2.Send(context.Background(), "again"); err != nil || reason.Kind != session.EndDone {
		t.Fatalf("second turn: %s %v", reason, err)
	}
}

func TestModelFailureEndsTurn(t *testing.T) {
	a := newAgent(t, tempStore(t), agentOpts{model: fakeModel{fail: true}})
	reason, err := a.Send(context.Background(), "ping")
	if err != nil || reason.Kind != session.EndFailed {
		t.Fatalf("reason=%s err=%v", reason, err)
	}
	if a.Core.Status() != session.StatusIdle {
		t.Fatal("失败后应回到 Idle 可继续")
	}
}

func TestNoHubIsError(t *testing.T) {
	a := &Agent{Model: fakeModel{}, SessionID: "s1", Core: session.New(testCfg())}
	if _, err := a.Send(context.Background(), "ping"); err == nil || !strings.Contains(err.Error(), "Hub") {
		t.Fatalf("未配置 Hub 应报错: %v", err)
	}
}
