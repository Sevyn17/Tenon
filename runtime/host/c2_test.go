package host

// C2（Mortise docs/76）：回投 notice 不收窄用户下一轮的权限；启动扫描不论状态打开有可唤醒信件的会话；
// StartSession 写创建链并拒绝第三层。

import (
	"context"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/runtime/mail"
	"github.com/Sevyn17/tenon/runtime/store"
)

func waitUntil(t *testing.T, what string, f func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if f() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等不到：%s", what)
}

// 被派会话回投（notice，caps 很窄）之后，用户在创建者会话里发消息：这一轮的 caps 不受回投影响（D2）。
func TestReplyNoticeDoesNotNarrowUserTurn(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, nil, &fakeModel{})
	hd, err := r.h.Open(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	admitEnvelope(t, r.db, "p", mail.Envelope{ID: "rp-x", From: mail.Address{Kind: mail.AddrSession, ID: "ag-1"},
		To: mail.Address{Kind: mail.AddrSession, ID: "p"}, Kind: mail.KindNotify, Mode: mail.ModeNotice,
		Caps: []string{}, Body: "子会话做完了"})
	if err := hd.Do(Command{Kind: Deliver}); err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	// 用户发 "tool"：fakeModel 调 echo；caps 若被空集收窄会被 caps 守卫拒绝，echo 不会执行。
	// echo 是 Dangerous，要确认：askGate 会挂起，这里改用 Confirm 放行。
	hd.Agent().InstallCapsGuard()
	if err := hd.Do(Command{Kind: Send, Text: "tool"}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "等确认", func() bool {
		hd.a.mu.Lock()
		defer hd.a.mu.Unlock()
		return len(hd.a.waiters) > 0
	})
	hd.a.mu.Lock()
	var cid string
	for k := range hd.a.waiters {
		cid = k
	}
	hd.a.mu.Unlock()
	if err := hd.Do(Command{Kind: Confirm, CallID: cid, Approve: true}); err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	if *r.runs != 1 {
		t.Fatalf("用户这一轮的 echo 应执行（caps 不受回投收窄）：runs=%d", *r.runs)
	}
}

// 启动扫描：状态停在 active（上次进程退出时没来得及休眠）、信箱有 input 的会话也被打开处理（D13）。
func TestScanMailOpensActiveSessions(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, nil, &fakeModel{})
	hd, err := r.h.Open(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := hd.Do(Command{Kind: Close}); err != nil {
		t.Fatal(err)
	}
	<-hd.Done()
	// 模拟上次进程：信落进信箱但没人处理，状态仍是 active。
	admitEnvelope(t, r.db, "s1", mail.Envelope{From: mail.Address{Kind: mail.AddrHuman, ID: "u1"},
		To: mail.Address{Kind: mail.AddrSession, ID: "s1"}, Kind: mail.KindRequest, Body: "重启前的信"})
	if st, _ := r.db.SessionStatus(ctx, "s1"); st != store.StatusActive {
		t.Fatalf("前提：状态应为 active，得 %s", st)
	}
	n, err := r.h.ScanMail(ctx)
	if err != nil || n != 1 {
		t.Fatalf("ScanMail 应打开 1 个会话：%d %v", n, err)
	}
	h2, ok := r.h.Get("s1")
	if !ok {
		t.Fatal("会话应已打开")
	}
	if err := h2.Wait(); err != nil {
		t.Fatal(err)
	}
	items, _ := r.db.Inbox(ctx, "s1")
	if wakeable(items) {
		t.Fatal("信箱里的信应已被处理")
	}
}

// StartSession：独立会话写创建链与回投方式；派出方已在第二层时拒绝（第三层）。
func TestStartSessionChainAndDepth(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, nil, &fakeModel{})
	if _, err := r.h.Open(ctx, "p"); err != nil {
		t.Fatal(err)
	}
	id, err := r.h.StartSession(ctx, StartOptions{Creator: "p", Task: "查", ReplyMode: ReplyNone})
	if err != nil {
		t.Fatal(err)
	}
	l, err := r.h.head(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if l.Meta.ParentID != "" || l.Meta.CreatorSession != "p" || l.Meta.Depth() != 2 || l.Meta.Root() != "p" ||
		l.Meta.ReplyMode != ReplyNone || l.Meta.Origin != "agent" {
		t.Fatalf("独立会话字段：%+v", l.Meta)
	}
	if _, err := r.h.StartSession(ctx, StartOptions{Creator: id, Task: "再派", ReplyMode: ReplyNone}); err == nil {
		t.Fatal("第二层再派应拒绝")
	}
	if _, err := r.h.StartSession(ctx, StartOptions{Creator: "p", Task: "x", ReplyMode: "wait"}); err == nil {
		t.Fatal("StartSession 不接受 wait")
	}
}
