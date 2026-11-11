package host

// 04 4.3 Deliver 中间件链在真实 Host 上的行为：拦截、改写、不能绕过守卫。

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/mail"
)

// 中间件短路：信封不进信箱。
func TestDeliverMiddlewareBlocks(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	hd, err := r.h.Open(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	_ = hd.Wait()
	kernel := r.h.DeliverKernel()
	r.h.SetDeliverChain(func(ctx context.Context, req component.DeliverReq) (component.DeliverResp, error) {
		_ = kernel
		return component.DeliverResp{Delivered: false}, nil
	})
	if err := r.h.Deliver(ctx, mail.Envelope{
		From: mail.Address{Kind: mail.AddrHuman, ID: "u1"},
		To:   mail.Address{Kind: mail.AddrSession, ID: "s1"},
		Kind: mail.KindRequest, Body: "should be blocked",
	}); err != nil {
		t.Fatal(err)
	}
	_ = hd.Wait()
	if items, _ := r.db.Inbox(ctx, "s1"); len(items) != 0 {
		t.Fatalf("被中间件拦下的信不该进信箱：%+v", items)
	}
}

// 中间件改写：信封被改到别的会话，目标会话跑到这一轮。
func TestDeliverMiddlewareReroutes(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	hd1, err := r.h.Open(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	_ = hd1.Wait()
	hd2, err := r.h.Open(ctx, "s2")
	if err != nil {
		t.Fatal(err)
	}
	_ = hd2.Wait()

	kernel := r.h.DeliverKernel()
	r.h.SetDeliverChain(func(ctx context.Context, req component.DeliverReq) (component.DeliverResp, error) {
		req.Envelope.To = mail.Address{Kind: mail.AddrSession, ID: "s2"}
		req.SessionID = "s2"
		return kernel(ctx, req)
	})
	if err := r.h.Deliver(ctx, mail.Envelope{
		From: mail.Address{Kind: mail.AddrHuman, ID: "u1"},
		To:   mail.Address{Kind: mail.AddrSession, ID: "s1"},
		Kind: mail.KindRequest, Body: "rerouted",
	}); err != nil {
		t.Fatal(err)
	}
	_ = hd2.Wait()
	if ms := messages(t, hd2); len(ms) == 0 {
		t.Fatal("中间件改写后 s2 应收到信并跑一轮")
	}
	// s1 不该收到。
	if items, _ := r.db.Inbox(ctx, "s1"); len(items) != 0 {
		t.Fatalf("改写后 s1 不该收到：%+v", items)
	}
}

// 中间件不能绕过守卫：最内层仍在跑（用跳数超限验证）。
func TestDeliverMiddlewareCannotBypassGuard(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	hd, err := r.h.Open(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	_ = hd.Wait()
	r.h.MaxHop = 2
	kernel := r.h.DeliverKernel()
	r.h.SetDeliverChain(func(ctx context.Context, req component.DeliverReq) (component.DeliverResp, error) {
		return kernel(ctx, req)
	})
	// Hop=2 再投 → 3 > 2，应被内核拒。
	err = r.h.deliverQueued(ctx, mail.Envelope{
		From: mail.Address{Kind: mail.AddrHuman, ID: "u1"},
		To:   mail.Address{Kind: mail.AddrSession, ID: "s1"},
		Kind: mail.KindRequest, Hop: 2, Body: "x",
	})
	if err == nil {
		t.Fatal("中间件放行后内核仍应判跳数超限")
	}
	_ = time.Now()
}
