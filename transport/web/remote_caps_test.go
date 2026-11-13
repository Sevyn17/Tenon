package web

// 第 8 项安全修复的复核：远端入站信封的提权路径。

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/Sevyn17/tenon/runtime/agent"
	"github.com/Sevyn17/tenon/runtime/host"
	"github.com/Sevyn17/tenon/runtime/mail"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store/memstore"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// caps 经 handler 不被丢弃（Host.DeliverRemote 也不清，故整条链保留）。
func TestRemoteCapsPreserved(t *testing.T) {
	var got mail.Envelope
	srv := httptest.NewServer(RemoteHandler(func(ctx context.Context, env mail.Envelope) error {
		got = env
		return nil
	}))
	defer srv.Close()
	if err := (&RemoteClient{}).Send(context.Background(), srv.URL, mail.Envelope{
		From: mail.Address{Kind: mail.AddrComponent, ID: "peer"},
		To:   mail.Address{Kind: mail.AddrSession, ID: "s1"},
		Kind: mail.KindRequest, Body: "x",
		Caps: []string{"only_this_tool"},
	}); err != nil {
		t.Fatal(err)
	}
	if len(got.Caps) != 1 || got.Caps[0] != "only_this_tool" {
		t.Fatalf("caps 不该被丢，实际 %v", got.Caps)
	}
}

// DeliverRemote 拒绝 from=human（远端不能冒充本地已鉴权用户）。
func TestDeliverRemoteRejectsHuman(t *testing.T) {
	h := newTestHost(t)
	err := h.DeliverRemote(context.Background(), mail.Envelope{
		From: mail.Address{Kind: mail.AddrHuman, ID: "u1"},
		To:   mail.Address{Kind: mail.AddrSession, ID: "s1"},
		Kind: mail.KindRequest, Body: "x",
	})
	if err == nil {
		t.Fatal("远端 from=human 应被拒")
	}
}

// DeliverRemote 把对端声称的组件身份降级后仍能正常投递（联系名单不认它）。
func TestDeliverRemoteDowngradesComponent(t *testing.T) {
	h := newTestHost(t)
	if err := h.DeliverRemote(context.Background(), mail.Envelope{
		From: mail.Address{Kind: mail.AddrComponent, ID: "hijacked"},
		To:   mail.Address{Kind: mail.AddrSession, ID: "s1"},
		Kind: mail.KindRequest, Body: "x",
	}); err != nil {
		t.Fatalf("降级后应能正常投递：%v", err)
	}
}

// DeliverRemote 对不存在的会话报错（不静默吞掉）。
func TestDeliverRemoteUnknownSession(t *testing.T) {
	h := newTestHost(t)
	if err := h.DeliverRemote(context.Background(), mail.Envelope{
		From: mail.Address{Kind: mail.AddrComponent, ID: "peer"},
		To:   mail.Address{Kind: mail.AddrSession, ID: "no-such"},
		Kind: mail.KindRequest, Body: "x",
	}); err == nil {
		t.Fatal("不存在的会话应报错")
	}
}

// newTestHost 建一个真实 Host（内存存储 + 最小会话构造），并开一个会话 s1。
func newTestHost(t *testing.T) *host.Host {
	t.Helper()
	ctx := context.Background()
	db := memstore.New()
	t.Cleanup(func() { db.Close() })
	h := &host.Host{DB: db}
	h.New = func(id, def string) (*agent.Agent, *session.Config, error) {
		hub := toolhub.New(toolhub.Options{Store: agent.ToolStore{DB: db}})
		a := &agent.Agent{Store: db, SessionID: id, Hub: hub}
		return a, &session.Config{Model: "m", Tools: hub.Exposed(hub.SpecsFor(nil)), Policy: session.DefaultPolicy()}, nil
	}
	hd, err := h.Open(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	_ = hd.Wait()
	return h
}
