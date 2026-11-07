package host

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"tenon/runtime/agent"
	"tenon/runtime/callctx"
	"tenon/runtime/mail"
	"tenon/runtime/session"
	"tenon/runtime/store"
	"tenon/runtime/store/sqlite"
	"tenon/runtime/toolhub"
)

// B4：request / notify 随便带一个 corr 不能绕过联系名单；只有回信凭 corr 进门，且要通过核对。
func TestFakeCorrDoesNotBypassContact(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	mustOpen(t, r.h, "s1")
	from := mail.Address{Kind: mail.AddrAgent, ID: "evil"}
	to := mail.Address{Kind: mail.AddrSession, ID: "s1"}
	err := r.h.Deliver(ctx, mail.Envelope{From: from, To: to, Kind: mail.KindRequest, Corr: "made-up", Body: "INJECTED"})
	if !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("带假 corr 的 request 应被联系名单拒绝，得 %v", err)
	}
	err = r.h.Deliver(ctx, mail.Envelope{From: from, To: to, Kind: mail.KindReply, Corr: "made-up", Body: "INJECTED"})
	if !errors.Is(err, store.ErrCorrRejected) {
		t.Fatalf("假 corr 的回信应被 corr 核对拒绝，得 %v", err)
	}
	items, _ := r.db.Inbox(ctx, "s1")
	if len(items) != 0 {
		t.Fatalf("被拒信封不应入信箱：%+v", items)
	}
}

// B5：入口 Deliver 忽略调用方给的内核字段（hop / trace / caps），组件不能自称 human。
func TestDeliverIgnoresCallerKernelFields(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	mustOpen(t, r.h, "s1")
	env := mail.Envelope{ID: "e1", From: mail.Address{Kind: mail.AddrComponent, ID: "c"},
		To: mail.Address{Kind: mail.AddrSession, ID: "s1"}, Kind: mail.KindNotify, Mode: mail.ModeContext,
		Hop: -1000, Trace: "victim-trace", Caps: []string{"shell"}, Origin: mail.OriginHuman, Body: "x"}
	if err := r.h.Deliver(ctx, env); err != nil {
		t.Fatal(err)
	}
	items, err := r.db.Inbox(ctx, "s1")
	if err != nil || len(items) != 1 {
		t.Fatalf("应入信箱一封：%v %+v", err, items)
	}
	got, _ := parseEnvelope(items[0])
	if got.Hop != 1 {
		t.Fatalf("入口信封跳数应从 0 起算（投递后 1），得 %d", got.Hop)
	}
	if got.Trace == "victim-trace" || got.Trace == "" {
		t.Fatalf("trace 应由内核新分配，得 %q", got.Trace)
	}
	if got.Caps != nil {
		t.Fatalf("入口信封的 caps 应清空，得 %v", got.Caps)
	}
	if got.Origin != mail.OriginAgent {
		t.Fatalf("组件发来的信封 origin 最高 agent，得 %q", got.Origin)
	}
	if evs, _ := r.db.TraceEvents(ctx, "victim-trace"); len(evs) != 0 {
		t.Fatalf("不应往调用方指定的 trace 下记事件：%+v", evs)
	}
}

// B3：两封信封的 caps 没有共同工具时交集是空集，空集 = 一个都不许，不是「不限」。
func TestDisjointCapsDenyAll(t *testing.T) {
	if got := intersectCaps([]string{"read_file"}, []string{"web_fetch"}); got == nil || len(got) != 0 {
		t.Fatalf("不相交的 caps 交集应为非 nil 空集，得 %#v", got)
	}
	if got := intersectCaps(nil, nil); got != nil {
		t.Fatalf("两边都不限时交集仍不限，得 %#v", got)
	}
	ctx := context.Background()
	h, executed := capsHost(t)
	hd := mustOpen(t, h, "s1")
	for _, c := range [][]string{{"read_file"}, {"web_fetch"}} {
		// 同一权限主体的两封合并成一轮（caps 是链上内核字段，走发件箱派发路径带上）。
		if err := h.deliverQueued(ctx, mail.Envelope{From: mail.Address{Kind: mail.AddrHuman, ID: "u1"},
			To: mail.Address{Kind: mail.AddrSession, ID: "s1"}, Kind: mail.KindRequest, Mode: mail.ModeContext,
			Body: "tool", Caps: c}); err != nil {
			t.Fatal(err)
		}
	}
	if err := hd.Do(Command{Kind: Deliver}); err != nil {
		t.Fatal(err)
	}
	if err := h.deliverQueued(ctx, mail.Envelope{From: mail.Address{Kind: mail.AddrHuman, ID: "u1"},
		To: mail.Address{Kind: mail.AddrSession, ID: "s1"}, Kind: mail.KindRequest, Body: "tool",
		Caps: []string{"read_file"}}); err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	if *executed != 0 {
		t.Fatalf("caps 交集为空时工具不应执行，执行了 %d 次", *executed)
	}
	if got := toolResults(hd.Agent()); !strings.Contains(got, "不在本轮授权范围") {
		t.Fatalf("应回 caps 拒绝：%s", got)
	}
}

// B2（09 9.6 ①）：低权限会话发给高权限会话，后者不能执行前者没有的工具——
// caps 由内核按发送会话的有效权限写入（Agent.Outbound），不靠调用方填。
func TestOutboundCapsFromSenderPermissions(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "s.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	executed := new(int)
	// low 只有 read_file；high 有 echo。
	h := &Host{DB: db, New: func(id, def string) (*agent.Agent, *session.Config, error) {
		hub := toolhub.New(toolhub.Options{Store: agent.ToolStore{DB: db}})
		name := "echo"
		if id == "low" {
			name = "read_file"
		}
		if err := hub.Register(&toolhub.Tool{Spec: session.ToolSpec{Name: name, Schema: json.RawMessage(`{"type":"object"}`)},
			Risk: toolhub.Read, Call: func(context.Context, *toolhub.Cx, json.RawMessage) (toolhub.Output, error) {
				*executed++
				return toolhub.Text("echoed"), nil
			}}); err != nil {
			return nil, nil, err
		}
		ts := hub.SpecsFor(nil, 0)
		a := &agent.Agent{Store: db, SessionID: id, Model: &fakeModel{}, Hub: hub, Toolset: ts}
		return a, &session.Config{Model: "m", Tools: hub.Exposed(ts), Policy: session.DefaultPolicy()}, nil
	}}
	// high 由 low 创建（父会话），使 session:low → session:high 的投递过联系名单。
	if _, err := h.open(ctx, "low", "", h.New, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := h.open(ctx, "high", "", h.New, func(ag *agent.Agent, cfg *session.Config) error {
		_, err := ag.OpenAs(ctx, cfg, store.SessionMeta{ID: "high", ParentID: "low"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	low, _ := h.Get("low")
	high, _ := h.Get("high")
	// 调用方想塞一个更宽的 caps / 伪造 from：Outbound 一律覆盖。
	if err := low.Agent().Outbound(mail.Envelope{From: mail.Address{Kind: mail.AddrHuman, ID: "root"},
		To: mail.Address{Kind: mail.AddrSession, ID: "high"}, Kind: mail.KindReply, Corr: "c-high",
		Caps: []string{"echo", "shell"}, Body: "tool"}); err != nil {
		t.Fatal(err)
	}
	if err := db.IssueCorr(ctx, store.Pending{Corr: "c-high", SessionID: "high", ReplyTo: "session:low"}); err != nil {
		t.Fatal(err)
	}
	if err := low.Agent().Commit(ctx); err != nil {
		t.Fatal(err)
	}
	items, _ := db.Outbox(ctx, 0)
	if len(items) != 1 {
		t.Fatalf("应有一封外发：%+v", items)
	}
	var queued mail.Envelope
	_ = json.Unmarshal(items[0].Envelope, &queued)
	if queued.From.String() != "session:low" {
		t.Fatalf("from 应是发送会话，得 %s", queued.From)
	}
	// caps = 发送会话的可用工具集（含内核自带的 tool_search 等机制工具），不含调用方塞的 echo / shell。
	caps := "," + strings.Join(queued.Caps, ",") + ","
	if !strings.Contains(caps, ",read_file,") || strings.Contains(caps, ",echo,") || strings.Contains(caps, ",shell,") {
		t.Fatalf("caps 应是发送会话的可用工具，得 %v", queued.Caps)
	}
	if _, err := h.FlushOutbox(ctx); err != nil {
		t.Fatal(err)
	}
	if err := high.Wait(); err != nil {
		t.Fatal(err)
	}
	if *executed != 0 {
		t.Fatalf("高权限会话不应执行发送方没有的工具，执行了 %d 次", *executed)
	}
	if got := toolResults(high.Agent()); !strings.Contains(got, "不在本轮授权范围") {
		t.Fatalf("应回 caps 拒绝：%s", got)
	}
}

// B10：外部来源（origin=external）的信封触发的回合，内核把调用上下文标成不可信，
// 权限层据此把 allow 降为询问；下一个可信回合重设。
func TestExternalTurnMarksCtxUntrusted(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "s.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	var seen []bool
	h := &Host{DB: db, New: func(id, def string) (*agent.Agent, *session.Config, error) {
		hub := toolhub.New(toolhub.Options{Store: agent.ToolStore{DB: db}})
		if err := hub.Register(&toolhub.Tool{Spec: session.ToolSpec{Name: "echo", Schema: json.RawMessage(`{"type":"object"}`)},
			Risk: toolhub.Read, Call: func(_ context.Context, cx *toolhub.Cx, _ json.RawMessage) (toolhub.Output, error) {
				seen = append(seen, cx.Ctx.Untrusted())
				return toolhub.Text("echoed"), nil
			}}); err != nil {
			return nil, nil, err
		}
		a := &agent.Agent{Store: db, SessionID: id, Model: &fakeModel{}, Hub: hub,
			Ctx: callctx.New(callctx.Options{SessionID: id})}
		return a, &session.Config{Model: "m", Tools: hub.Exposed(hub.SpecsFor(nil, 0)), Policy: session.DefaultPolicy()}, nil
	}}
	hd := mustOpen(t, h, "s1")
	send := func(origin mail.Origin) {
		t.Helper()
		if err := h.Deliver(ctx, mail.Envelope{From: mail.Address{Kind: mail.AddrComponent, ID: "webhook"},
			To: mail.Address{Kind: mail.AddrSession, ID: "s1"}, Kind: mail.KindRequest, Origin: origin, Body: "tool"}); err != nil {
			t.Fatal(err)
		}
		if err := hd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	send(mail.OriginExternal)
	send(mail.OriginAgent)
	if len(seen) != 2 || !seen[0] || seen[1] {
		t.Fatalf("外部来源回合应标不可信、下一个可信回合重设：%v", seen)
	}
}

// capsHost 带一个 echo 工具（只读）的宿主；fakeModel 收到 "tool" 时调 echo。
func capsHost(t *testing.T) (*Host, *int) {
	t.Helper()
	db, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "s.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	executed := new(int)
	h := &Host{DB: db, New: func(id, def string) (*agent.Agent, *session.Config, error) {
		hub := toolhub.New(toolhub.Options{Store: agent.ToolStore{DB: db}})
		if err := hub.Register(&toolhub.Tool{Spec: session.ToolSpec{Name: "echo", Schema: json.RawMessage(`{"type":"object"}`)},
			Risk: toolhub.Read, Call: func(context.Context, *toolhub.Cx, json.RawMessage) (toolhub.Output, error) {
				*executed++
				return toolhub.Text("echoed"), nil
			}}); err != nil {
			return nil, nil, err
		}
		a := &agent.Agent{Store: db, SessionID: id, Model: &fakeModel{}, Hub: hub}
		return a, &session.Config{Model: "m", Tools: hub.Exposed(hub.SpecsFor(nil, 0)), Policy: session.DefaultPolicy()}, nil
	}}
	return h, executed
}
