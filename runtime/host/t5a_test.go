package host

// T5a：常驻定义——固定会话 id resident:<租户>/<名字>，投 agent:<名> 时查到就投、查不到就建；
// 两个节点（两个 Host）同时投只产生一个会话；重启后无待处理信封不唤醒（由 Open 决定）。

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Sevyn17/tenon/runtime/agentdef"
	"github.com/Sevyn17/tenon/runtime/mail"
	"github.com/Sevyn17/tenon/runtime/store"
)

// residentDefs 造一个含常驻定义的来源（平台层）。
func residentDefs(t *testing.T, name string) agentdef.Source {
	t.Helper()
	m := agentdef.Mem()
	man := "format = \"tenon/v1\"\nid = \"" + name + "\"\nresident = true\ndescription = \"常驻\"\n"
	if err := m.Put(agentdef.Ref{Namespace: agentdef.Platform, Name: name}, agentdef.Files{agentdef.ManifestName: []byte(man)}); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestResidentFixedSession(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	r.h.Definitions = residentDefs(t, "bot")
	// 两次投给 agent:bot（不带 key），应落到同一个常驻会话。
	id1, err := r.h.DeliverAgent(ctx, mail.Envelope{From: mail.Address{Kind: mail.AddrHuman, ID: "u1"},
		To: mail.Address{Kind: mail.AddrAgent, ID: "bot"}, Kind: mail.KindRequest, Body: "一"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := r.h.DeliverAgent(ctx, mail.Envelope{From: mail.Address{Kind: mail.AddrHuman, ID: "u2"},
		To: mail.Address{Kind: mail.AddrAgent, ID: "bot"}, Kind: mail.KindRequest, Body: "二"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "resident:" + store.Tenant + "/bot"
	if id1 != want || id2 != want {
		t.Fatalf("常驻会话 id = %q / %q，期望 %q", id1, id2, want)
	}
	l, err := r.db.Load(ctx, want, nil)
	if err != nil {
		t.Fatal(err)
	}
	if l.Meta.Creator != "system:"+store.Tenant {
		t.Fatalf("常驻会话创建者 = %q，期望 system:%s", l.Meta.Creator, store.Tenant)
	}
	if l.Meta.AgentID != "bot" {
		t.Fatalf("常驻会话定义 = %q", l.Meta.AgentID)
	}
}

// 两个 Host 同投同一常驻定义：仍只一个会话（固定 id + 唯一约束冲突转投）。
func TestResidentTwoHostsOneSession(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "s.db")
	r1 := newRig(t, path, &fakeModel{})
	defs := residentDefs(t, "bot")
	r1.h.Definitions = defs
	// 第二个 Host 共用同一存储。
	h2 := &Host{DB: r1.db, New: r1.h.New, Definitions: defs}
	_, err := r1.h.DeliverAgent(ctx, mail.Envelope{From: mail.Address{Kind: mail.AddrHuman, ID: "u1"},
		To: mail.Address{Kind: mail.AddrAgent, ID: "bot"}, Kind: mail.KindRequest, Body: "一"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h2.DeliverAgent(ctx, mail.Envelope{From: mail.Address{Kind: mail.AddrHuman, ID: "u2"},
		To: mail.Address{Kind: mail.AddrAgent, ID: "bot"}, Kind: mail.KindRequest, Body: "二"}, nil); err != nil {
		t.Fatal(err)
	}
	// 只有一个常驻会话行：按固定 id 能找到，且没有别的 ag- 会话。
	want := "resident:" + store.Tenant + "/bot"
	if _, err := r1.db.Load(ctx, want, nil); err != nil {
		t.Fatalf("应有常驻会话 %s：%v", want, err)
	}
}
