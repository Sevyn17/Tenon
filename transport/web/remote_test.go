package web

// T6a 端到端：两个 HTTP 端点互投信封（出站 RemoteClient + 入站 RemoteHandler）。

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/Sevyn17/tenon/runtime/mail"
)

func TestRemoteRoundTrip(t *testing.T) {
	var got mail.Envelope
	var gotErr error
	srv := httptest.NewServer(RemoteHandler(func(ctx context.Context, env mail.Envelope) error {
		got = env
		return gotErr
	}))
	defer srv.Close()

	c := &RemoteClient{Token: "t0ken"}
	err := c.Send(context.Background(), srv.URL, mail.Envelope{
		From:   mail.Address{Kind: mail.AddrComponent, ID: "peer"},
		To:     mail.Address{Kind: mail.AddrRemote, Host: srv.URL, ID: "worker"},
		Kind:   mail.KindRequest,
		Mode:   mail.ModeWake,
		Origin: mail.OriginExternal,
		Body:   "hello",
	})
	if err != nil {
		t.Fatalf("出站投递：%v", err)
	}
	if got.Body != "hello" || got.To.ID != "worker" {
		t.Fatalf("入站收到 %+v", got)
	}
}

// 入站拒绝 from=human（远端不能冒充本地已鉴权用户，08 8.5）。
func TestRemoteRejectsHumanFrom(t *testing.T) {
	srv := httptest.NewServer(RemoteHandler(func(ctx context.Context, env mail.Envelope) error { return nil }))
	defer srv.Close()
	c := &RemoteClient{}
	err := c.Send(context.Background(), srv.URL, mail.Envelope{
		From: mail.Address{Kind: mail.AddrHuman, ID: "u1"},
		To:   mail.Address{Kind: mail.AddrRemote, Host: srv.URL, ID: "worker"},
		Kind: mail.KindRequest, Body: "spoof",
	})
	if err == nil {
		t.Fatal("from=human 的远端信封应被拒")
	}
}

// 对端返回错误时出站返回错误。
func TestRemotePropagatesError(t *testing.T) {
	boom := errors.New("投递被拒")
	srv := httptest.NewServer(RemoteHandler(func(ctx context.Context, env mail.Envelope) error { return boom }))
	defer srv.Close()
	c := &RemoteClient{}
	err := c.Send(context.Background(), srv.URL, mail.Envelope{
		From: mail.Address{Kind: mail.AddrComponent, ID: "peer"},
		To:   mail.Address{Kind: mail.AddrRemote, Host: srv.URL, ID: "worker"},
		Kind: mail.KindRequest, Body: "x",
	})
	if err == nil {
		t.Fatal("对端错误应传回")
	}
}

// 缺 host 报错。
func TestRemoteMissingHost(t *testing.T) {
	c := &RemoteClient{}
	if err := c.Send(context.Background(), "", mail.Envelope{}); err == nil {
		t.Fatal("缺 host 应报错")
	}
}
