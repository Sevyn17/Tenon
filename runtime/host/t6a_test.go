package host

// T6a：remote: 地址经 Host.Remote 出站；Agent Card 只导公开字段。

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Sevyn17/tenon/runtime/agentdef"
	"github.com/Sevyn17/tenon/runtime/mail"
	"github.com/Sevyn17/tenon/runtime/store"
)

type fakeRemote struct {
	host string
	env  mail.Envelope
}

func (f *fakeRemote) Send(ctx context.Context, host string, env mail.Envelope) error {
	f.host, f.env = host, env
	return nil
}

func TestDeliverRemoteViaSender(t *testing.T) {
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	fr := &fakeRemote{}
	r.h.Remote = fr
	err := r.h.Deliver(context.Background(), mail.Envelope{
		From: mail.Address{Kind: mail.AddrHuman, ID: "u1"},
		To:   mail.Address{Kind: mail.AddrRemote, Host: "peer:9000", ID: "worker"},
		Kind: mail.KindRequest, Body: "hi",
	})
	if err != nil {
		t.Fatal(err)
	}
	if fr.host != "peer:9000" || fr.env.Body != "hi" {
		t.Fatalf("远端出站 = %q %q", fr.host, fr.env.Body)
	}
}

func TestDeliverRemoteNoSender(t *testing.T) {
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	err := r.h.Deliver(context.Background(), mail.Envelope{
		From: mail.Address{Kind: mail.AddrHuman, ID: "u1"},
		To:   mail.Address{Kind: mail.AddrRemote, Host: "peer", ID: "worker"},
		Kind: mail.KindRequest, Body: "hi",
	})
	if err == nil {
		t.Fatal("没配出站传输应报错")
	}
}

func TestAgentCardNoLeak(t *testing.T) {
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	m := agentdef.Mem()
	man := "format = \"tenon/v1\"\nid = \"bot\"\ndescription = \"客服\"\nprompt = \"绝密提示词\"\ntools = [\"a\",\"b\"]\n"
	if err := m.Put(agentdef.Ref{Namespace: agentdef.Platform, Name: "bot"}, agentdef.Files{agentdef.ManifestName: []byte(man)}); err != nil {
		t.Fatal(err)
	}
	r.h.Definitions = m
	card, err := r.h.AgentCard("bot")
	if err != nil {
		t.Fatal(err)
	}
	if card.ID != "bot" || card.Description != "客服" || len(card.Tools) != 2 {
		t.Fatalf("card = %+v", card)
	}
	_ = store.Tenant
}
