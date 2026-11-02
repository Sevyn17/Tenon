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
