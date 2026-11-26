package sessions

// 创建链两层（Mortise docs/76 C1）：子会话记 creator_session 与深度；第二层再建会话在宿主与工具两处都被拒；
// 联系策略接入点：有提供方时按它判定，没有时只看定义名单。

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/runtime/agent"
	"github.com/Sevyn17/tenon/runtime/callctx"
	"github.com/Sevyn17/tenon/runtime/host"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
	"github.com/Sevyn17/tenon/runtime/store/memstore"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

func chainHost(t *testing.T) (*host.Host, *memstore.Store) {
	t.Helper()
	db := memstore.New()
	t.Cleanup(func() { db.Close() })
	h := &host.Host{Workspace: "w", DB: db}
	h.New = func(id, def string) (*agent.Agent, *session.Config, error) {
		hub := toolhub.New(toolhub.Options{Store: agent.ToolStore{DB: db}})
		return &agent.Agent{Store: db, SessionID: id, Model: echoModel{}, Hub: hub},
			&session.Config{Model: "m", Policy: session.DefaultPolicy()}, nil
	}
	return h, db
}

func TestChainRecordedAndDepthLimit(t *testing.T) {
	ctx := context.Background()
	h, db := chainHost(t)
	if _, _, err := h.SpawnWith(ctx, "p", "p/sub-1", Origin, "", h.New, "做一件事", false, host.SpawnOptions{}); err != nil {
		t.Fatal(err)
	}
	l, err := host.LoadHead(ctx, db, "p/sub-1")
	if err != nil {
		t.Fatal(err)
	}
	if l.Meta.CreatorSession != "p" || l.Meta.Depth() != 2 || l.Meta.Root() != "p" || l.Meta.Origin != Origin {
		t.Fatalf("创建链字段不对：%+v", l.Meta)
	}
	// 第二层再派：宿主拒绝。
	_, _, err = h.SpawnWith(ctx, "p/sub-1", "p/sub-1/sub-2", Origin, "", h.New, "再派", false, host.SpawnOptions{})
	if !errors.Is(err, host.ErrChainDepth) {
		t.Fatalf("第二层再建会话应报 ErrChainDepth：%v", err)
	}
	if ok, _ := db.Exists(ctx, "p/sub-1/sub-2"); ok {
		t.Fatal("被拒时不应留下会话行")
	}
}

func TestToolRejectsAtSecondLayer(t *testing.T) {
	ctx := context.Background()
	h, _ := chainHost(t)
	tool := ToolWithParams(h, h.New, nil, Params{})
	kc := callctx.New(callctx.Options{SessionID: "p/sub-1", Child: true})
	kc.Bind(callctx.Session{ID: "p/sub-1", CreatorSession: "p", ChainDepth: 2}, nil, nil)
	out, err := tool.Call(ctx, &toolhub.Cx{SessionID: "p/sub-1", CallID: "call-00000009", Tool: tool, Ctx: kc}, json.RawMessage(`{"task":"再派"}`))
	if err != nil {
		t.Fatal(err)
	}
	if out.Status.State == "ok" || !strings.Contains(contentText(out.Content), "第 2 层") {
		t.Fatalf("第二层调用应被拒并说明：%+v", out)
	}
}

type denyAll struct{ calls int }

func (d *denyAll) CanStart(_ context.Context, from, to string) error {
	d.calls++
	return errors.New(to + " 不可被 " + from + " 发现")
}

func TestContactPolicyHook(t *testing.T) {
	ctx := context.Background()
	h, _ := chainHost(t)
	pol := &denyAll{}
	tool := ToolWithContacts(h, h.New, nil, Params{}, pol)
	kc := callctx.New(callctx.Options{SessionID: "p"})
	kc.Bind(callctx.Session{ID: "p", Definition: "main", ChainDepth: 1}, nil, json.RawMessage(`{"session-start":{"agents":["reviewer"]}}`))
	call := func(cid, args string) toolhub.Output {
		out, err := tool.Call(ctx, &toolhub.Cx{SessionID: "p", CallID: cid, Tool: tool, Ctx: kc}, json.RawMessage(args))
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	// 名册智能体：问策略，被拒。
	if out := call("call-0000000a", `{"task":"审","agent":"reviewer"}`); out.Status.State == "ok" || !strings.Contains(contentText(out.Content), "不可被 main 发现") {
		t.Fatalf("策略拒绝应原样返回：%+v", out)
	}
	if pol.calls != 1 {
		t.Fatalf("策略应被问 1 次：%d", pol.calls)
	}
	// 临时智能体（agent 留空）不经策略。
	if out := call("call-0000000b", `{"task":"查"}`); out.Status.State != "ok" {
		t.Fatalf("临时智能体应照常：%+v", out)
	}
	if pol.calls != 1 {
		t.Fatalf("临时智能体不应问策略：%d", pol.calls)
	}
	_ = store.MaxChainDepth
}
