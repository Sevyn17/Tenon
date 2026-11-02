package host

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"ybase-kernel/core/agent"
	"ybase-kernel/core/mail"
	"ybase-kernel/core/session"
	"ybase-kernel/core/store"
	"ybase-kernel/core/store/sqlite"
	"ybase-kernel/core/toolhub"
)

// admitEnvelope 直接往 inbox 写一封信封（绕过投递守卫，测信箱合并语义）。
func admitEnvelope(t *testing.T, db *sqlite.DB, sessionID string, env mail.Envelope) {
	t.Helper()
	if err := env.Normalize(); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(session.Content{session.NewTextPart(env.Body)})
	eb, _ := json.Marshal(env)
	kind := store.InboxInput
	switch env.Mode {
	case mail.ModeContext:
		kind = store.InboxContext
	case mail.ModeNotice:
		kind = store.InboxNotice
	}
	if _, err := db.Admit(context.Background(), sessionID, store.InboxItem{ItemID: env.ID, Kind: kind,
		Input: string(body), Trust: "trusted", Envelope: string(eb)}); err != nil {
		t.Fatal(err)
	}
}

func TestInboxMergeByPermissionSubject(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	hd, err := r.h.Open(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	// 同一权限主体（from=u1、无 budget_ref）的两封 wake + 一封 context：合并成一条用户消息。
	admitEnvelope(t, r.db, "s1", mail.Envelope{From: mail.Address{Kind: mail.AddrHuman, ID: "u1"},
		To: mail.Address{Kind: mail.AddrSession, ID: "s1"}, Kind: mail.KindRequest, Body: "第一条"})
	admitEnvelope(t, r.db, "s1", mail.Envelope{From: mail.Address{Kind: mail.AddrHuman, ID: "u1"},
		To: mail.Address{Kind: mail.AddrSession, ID: "s1"}, Kind: mail.KindRequest, Mode: mail.ModeContext, Body: "背景"})
	admitEnvelope(t, r.db, "s1", mail.Envelope{From: mail.Address{Kind: mail.AddrHuman, ID: "u1"},
		To: mail.Address{Kind: mail.AddrSession, ID: "s1"}, Kind: mail.KindRequest, Body: "第二条"})
	// 另一个权限主体：应留到下一回合。
	admitEnvelope(t, r.db, "s1", mail.Envelope{From: mail.Address{Kind: mail.AddrHuman, ID: "u2"},
		To: mail.Address{Kind: mail.AddrSession, ID: "s1"}, Kind: mail.KindRequest, Body: "别人的"})
	if err := hd.Do(Command{Kind: Deliver}); err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	if r.m.calls != 2 { // u1 一条、u2 一条
		t.Fatalf("合并后应两轮，实际 calls=%d", r.m.calls)
	}
	first := textOfMsg(t, agent.History(hd.Agent().Core)[0])
	for _, want := range []string{`from="u1"`, "第一条", `from="u1"`, "背景", `from="u1"`, "第二条"} {
		if !strings.Contains(first, want) {
			t.Fatalf("第一轮缺 %q：%s", want, first)
		}
	}
	if strings.Contains(first, "别人的") {
		t.Fatalf("不同权限主体不应合并：%s", first)
	}
	second := textOfMsg(t, agent.History(hd.Agent().Core)[2])
	if !strings.Contains(second, "别人的") || strings.Contains(second, "第一条") {
		t.Fatalf("第二轮: %s", second)
	}
}

func TestDeliverContextWaitsForWake(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	hd, err := r.h.Open(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := r.h.Deliver(ctx, mail.Envelope{From: mail.Address{Kind: mail.AddrHuman, ID: "u1"},
		To: mail.Address{Kind: mail.AddrSession, ID: "s1"}, Kind: mail.KindNotify,
		Mode: mail.ModeContext, Body: "背景资料"}); err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	if r.m.calls != 0 {
		t.Fatalf("context 不应开回合：calls=%d", r.m.calls)
	}
	if err := r.h.Deliver(ctx, mail.Envelope{From: mail.Address{Kind: mail.AddrHuman, ID: "u1"},
		To: mail.Address{Kind: mail.AddrSession, ID: "s1"}, Kind: mail.KindRequest, Body: "开始"}); err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	if r.m.calls != 1 {
		t.Fatalf("wake 应开一轮：calls=%d", r.m.calls)
	}
	u := textOfMsg(t, agent.History(hd.Agent().Core)[0])
	if !strings.Contains(u, "背景资料") || !strings.Contains(u, "开始") {
		t.Fatalf("背景应随 wake 一起进模型：%s", u)
	}
}

func TestDeliverNoticeBecomesStatusEntry(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	hd, err := r.h.Open(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := r.h.Deliver(ctx, mail.Envelope{From: mail.Address{Kind: mail.AddrComponent, ID: "permission"},
		To: mail.Address{Kind: mail.AddrSession, ID: "s1"}, Kind: mail.KindNotify, Body: "档位已改为 plan"}); err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	if r.m.calls != 0 {
		t.Fatalf("notice 不应开回合：calls=%d", r.m.calls)
	}
	items, err := r.db.Inbox(ctx, "s1")
	if err != nil || len(items) != 0 {
		t.Fatalf("notice 应已消费：%v %v", items, err)
	}
	// 下一次用户输入应能看到状态更新条目。
	if err := hd.Do(Command{Kind: Send, Text: "继续"}); err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	var statusText string
	for _, e := range hd.Agent().Core.Snapshot().Entries {
		if e.Item.Kind == session.KindStatus && e.Item.Injection != nil {
			statusText += e.Item.Injection.Text
		}
	}
	if !strings.Contains(statusText, `<status-update>`) || !strings.Contains(statusText, "档位已改为 plan") {
		t.Fatalf("状态条目: %q", statusText)
	}
}

func TestDeliverReplyCorrCheck(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	hd, err := r.h.Open(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := r.db.IssueCorr(ctx, store.Pending{Corr: "c1", SessionID: "s1", ReplyTo: "agent:worker"}); err != nil {
		t.Fatal(err)
	}
	reply := func(from, corr string) error {
		return r.h.Deliver(ctx, mail.Envelope{From: mail.Address{Kind: mail.AddrAgent, ID: from},
			To: mail.Address{Kind: mail.AddrSession, ID: "s1"}, Kind: mail.KindReply, Corr: corr, Body: "结果"})
	}
	if err := reply("other", "c1"); !errors.Is(err, store.ErrCorrRejected) {
		t.Fatalf("伪造发送者应拒: %v", err)
	}
	if err := reply("worker", "c1"); err != nil {
		t.Fatalf("正确回信: %v", err)
	}
	if err := reply("worker", "c1"); !errors.Is(err, store.ErrCorrRejected) {
		t.Fatalf("重放应拒: %v", err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	u := textOfMsg(t, agent.History(hd.Agent().Core)[0])
	if !strings.Contains(u, `from="worker"`) || !strings.Contains(u, "结果") {
		t.Fatalf("回信内容: %s", u)
	}
}

func TestDeliverControlOnlyCreator(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{block: make(chan struct{})})
	hd, err := r.h.open(ctx, "s1", "", r.h.New, func(ag *agent.Agent, cfg *session.Config) error {
		_, err := ag.OpenAs(ctx, cfg, store.SessionMeta{ID: "s1", Creator: "u1"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := hd.Do(Command{Kind: Send, Text: "hello"}); err != nil { // 模型阻塞，会话在回合中
		t.Fatal(err)
	}
	ctrl := func(from, body string) error {
		return r.h.Deliver(ctx, mail.Envelope{From: mail.Address{Kind: mail.AddrHuman, ID: from},
			To: mail.Address{Kind: mail.AddrSession, ID: "s1"}, Kind: mail.KindControl, Body: body})
	}
	if err := ctrl("u2", "interrupt"); err == nil {
		t.Fatal("非创建者的 control 应被拒")
	}
	if err := ctrl("u1", "interrupt"); err != nil {
		t.Fatalf("创建者 interrupt: %v", err)
	}
	close(r.m.block)
	if err := hd.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := r.h.Deliver(ctx, mail.Envelope{From: mail.Address{Kind: mail.AddrHuman, ID: "u1"},
		To: mail.Address{Kind: mail.AddrSession, ID: "s1"}, Kind: mail.KindControl, Body: "close"}); err != nil {
		t.Fatalf("创建者 close: %v", err)
	}
	if err := hd.Wait(); err == nil {
		// 已关闭的 actor 退出，Wait 返回 ErrClosed 或 nil 均可；用 Get 判断。
	}
	if _, ok := r.h.Get("s1"); ok {
		t.Fatal("close 后 actor 应退出")
	}
}

func TestRewindVoidsCorr(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	hd, err := r.h.Open(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if err := hd.Do(Command{Kind: Send, Text: "一"}); err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	// 第二轮再发一条协程（签发位置在日志末尾之后）。
	if err := hd.Do(Command{Kind: Send, Text: "二"}); err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	hidx := hd.Agent().Core.Snapshot().Header.LogLen // 4
	if err := r.db.IssueCorr(ctx, store.Pending{Corr: "old", SessionID: "s1", ReplyTo: "agent:a", IssuedIdx: 0}); err != nil {
		t.Fatal(err)
	}
	if err := r.db.IssueCorr(ctx, store.Pending{Corr: "new", SessionID: "s1", ReplyTo: "agent:a", IssuedIdx: hidx}); err != nil {
		t.Fatal(err)
	}
	// 回退到第一轮：之后签发的新 corr 作废；更早的仍有效。
	if err := hd.Do(Command{Kind: Rewind, To: 1}); err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.ClaimCorr(ctx, "new", "s1", "agent:a"); !errors.Is(err, store.ErrCorrRejected) {
		t.Fatalf("回退点后的 corr 应作废: %v", err)
	}
	if _, err := r.db.ClaimCorr(ctx, "old", "s1", "agent:a"); err != nil {
		t.Fatalf("更早签发的 corr 仍有效: %v", err)
	}
}

func textOfMsg(t *testing.T, m session.Message) string {
	t.Helper()
	var b strings.Builder
	for _, p := range m.Content {
		b.WriteString(p.Text)
	}
	return b.String()
}

// TestDeliverAgentCreatesAndReusesKey：agent: 地址不带 key 每次新建；带 key 按
// （定义, 创建者, key）查或建（13 13.3 B19）。
func TestDeliverAgentCreatesAndReusesKey(t *testing.T) {
	db, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "s.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	var defs []string
	h := &Host{DB: db, New: func(id, def string) (*agent.Agent, *session.Config, error) {
		defs = append(defs, def)
		a := &agent.Agent{Store: db, SessionID: id, Model: &fakeModel{}}
		cfg := &session.Config{Model: "m", Policy: session.DefaultPolicy()}
		return a, cfg, nil
	}}
	ctx := context.Background()
	mk := func(key string) mail.Envelope {
		return mail.Envelope{From: mail.Address{Kind: mail.AddrHuman, ID: "u1"},
			To: mail.Address{Kind: mail.AddrAgent, ID: "worker"}, Key: key, Body: "你好"}
	}
	send := func(key string) {
		t.Helper()
		if err := h.Deliver(ctx, mk(key)); err != nil {
			t.Fatalf("Deliver: %v", err)
		}
	}
	send("k1")
	id1, err := db.FindSessionByKey(ctx, "worker", "u1", "k1")
	if err != nil {
		t.Fatalf("FindSessionByKey: %v", err)
	}
	hd, ok := h.Get(id1)
	if !ok {
		t.Fatalf("会话 %s 应已打开", id1)
	}
	if err := hd.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	send("k1") // 同键复用
	hd.Wait()
	if list, _ := db.ListSessions(ctx, 0, 50, false); len(list) != 1 {
		t.Fatalf("同键应该只有 1 个会话，得到 %d", len(list))
	}
	send("") // 不带 key：新建
	if list, _ := db.ListSessions(ctx, 0, 50, false); len(list) != 2 {
		t.Fatalf("不带 key 应该新建会话，得到 %d", len(list))
	}
	for _, d := range defs {
		if d != "worker" {
			t.Fatalf("Factory 定义名 = %q，期望 worker", d)
		}
	}
}

// TestGuardContactAndHop：联系名单第一版 + 跳数上限（08 D4）。
func TestGuardContactAndHop(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	if _, err := r.h.open(ctx, "s1", "", r.h.New, func(ag *agent.Agent, cfg *session.Config) error {
		_, err := ag.OpenAs(ctx, cfg, store.SessionMeta{ID: "s1", Creator: "u1"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	send := func(from mail.Address, hop int) error {
		t.Helper()
		return r.h.Deliver(ctx, mail.Envelope{From: from, To: mail.Address{Kind: mail.AddrSession, ID: "s1"}, Body: "hi", Hop: hop})
	}
	if err := send(mail.Address{Kind: mail.AddrHuman, ID: "u2"}, 0); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("非创建者应被拒，得到 %v", err)
	}
	if err := send(mail.Address{Kind: mail.AddrHuman, ID: "u1"}, 0); err != nil {
		t.Fatalf("创建者应允许：%v", err)
	}
	if err := send(mail.Address{Kind: mail.AddrAgent, ID: "worker"}, 0); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("agent 直投应被拒，得到 %v", err)
	}
	if err := send(mail.Address{Kind: mail.AddrComponent, ID: "permission"}, 0); err != nil {
		t.Fatalf("组件应允许：%v", err)
	}
	if err := send(mail.Address{Kind: mail.AddrHuman, ID: "u1"}, MaxHop); !errors.Is(err, ErrTooManyHops) {
		t.Fatalf("跳数超限应被拒，得到 %v", err)
	}
}

// TestOutboxDispatchedAfterCommit：外发与检查点同事务（08 9.6）——回合提交后
// 才派发出站信封，派发成功后发件箱清空。
func TestOutboxDispatchedAfterCommit(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	h1 := mustOpen(t, r.h, "s1")
	h2 := mustOpen(t, r.h, "s2")
	env := mail.Envelope{From: mail.Address{Kind: mail.AddrComponent, ID: "test"},
		To: mail.Address{Kind: mail.AddrSession, ID: "s2"}, Body: "外发消息"}
	if err := env.Normalize(); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(env)
	h1.Agent().EnqueueOutbox(env.ID, b)
	if err := h1.Do(Command{Kind: Send, Text: "你好"}); err != nil {
		t.Fatal(err)
	}
	if err := h1.Wait(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		for _, m := range messages(t, h2) {
			if strings.Contains(textOfMsg(t, m), "外发消息") {
				return true
			}
		}
		return false
	})
	items, err := r.h.DB.Outbox(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("派发后发件箱应为空，得到 %+v", items)
	}
}

// toolResults 把历史里的工具结果文本拼起来（caps 测试用）。
func toolResults(c *session.Core) string {
	var b strings.Builder
	for _, m := range agent.History(c) {
		if m.Role != "tool" {
			continue
		}
		for _, p := range m.Content {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// TestCapsRestrictToolCall 信封 caps 交集限制本轮可调用的工具（08 8.x / 13 13.3）：
// 模型请求范围外的工具时返回错误结果、不交给执行器；下一轮换新 caps 生效，回合结束清空。
func TestCapsRestrictToolCall(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "s.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	executed := new(int)
	m := &fakeModel{}
	h := &Host{DB: db, New: func(id, def string) (*agent.Agent, *session.Config, error) {
		hub := toolhub.New(toolhub.Options{Store: agent.ToolStore{DB: db}})
		echo := &toolhub.Tool{
			Spec: session.ToolSpec{Name: "echo", Schema: json.RawMessage(`{"type":"object"}`)},
			Risk: toolhub.Read,
			Call: func(context.Context, *toolhub.Cx, json.RawMessage) (toolhub.Output, error) {
				*executed++
				return toolhub.Text("echoed"), nil
			},
		}
		if err := hub.Register(echo); err != nil {
			return nil, nil, err
		}
		a := &agent.Agent{Store: db, SessionID: id, Model: m, Hub: hub}
		cfg := &session.Config{Model: "m", Tools: hub.Exposed(hub.SpecsFor(nil, 0)), Policy: session.DefaultPolicy()}
		return a, cfg, nil
	}}
	hd, err := h.Open(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	deliver := func(caps []string) {
		t.Helper()
		if err := h.Deliver(ctx, mail.Envelope{
			From: mail.Address{Kind: mail.AddrHuman, ID: "u1"},
			To:   mail.Address{Kind: mail.AddrSession, ID: "s1"},
			Kind: mail.KindRequest, Body: "tool", Caps: caps,
		}); err != nil {
			t.Fatal(err)
		}
		if err := hd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	deliver([]string{"read_file"}) // echo 不在范围
	if got := toolResults(hd.Agent().Core); !strings.Contains(got, "不在本轮授权范围") {
		t.Fatalf("超出 caps 的调用应被拒绝：%s", got)
	}
	if *executed != 0 {
		t.Fatalf("被 caps 拒绝的工具不应执行，实际执行 %d 次", *executed)
	}
	if got := hd.Agent().Caps(); len(got) != 0 {
		t.Fatalf("回合结束后 caps 应清空，得到 %v", got)
	}
	deliver([]string{"echo"}) // 放行
	got := toolResults(hd.Agent().Core)
	if !strings.Contains(got, "echoed") || *executed != 1 {
		t.Fatalf("caps 放行后应执行，执行 %d 次：%s", *executed, got)
	}
	if strings.Count(got, "不在本轮授权范围") != 1 {
		t.Fatalf("新一轮不该再被 caps 拒：%s", got)
	}
}
