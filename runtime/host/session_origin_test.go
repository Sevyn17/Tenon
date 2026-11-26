package host

import (
	"context"
	"testing"

	"github.com/Sevyn17/tenon/runtime/mail"
	"github.com/Sevyn17/tenon/runtime/store"
)

// TestSessionOrigin：组件发信可请求会话 origin（白名单内才认）；非组件或不在白名单的一律按 agent 建。
func TestSessionOrigin(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, nil, &fakeModel{})
	r.h.SessionOrigins = []string{"review"}
	cases := []struct {
		from mail.Address
		key  string
		req  string
		want string
	}{
		{mail.Address{Kind: mail.AddrComponent, ID: "evolve"}, "k1", "review", "review"},
		{mail.Address{Kind: mail.AddrComponent, ID: "evolve"}, "k2", "other", "agent"},
		{mail.Address{Kind: mail.AddrHuman, ID: "u1"}, "k3", "review", "agent"},
	}
	for _, c := range cases {
		if err := r.h.Deliver(ctx, mail.Envelope{From: c.from, To: mail.Address{Kind: mail.AddrAgent, ID: "worker"},
			Key: c.key, SessionOrigin: c.req, Body: "hi"}); err != nil {
			t.Fatalf("%s：%v", c.key, err)
		}
		creator := ""
		if c.from.Kind == mail.AddrHuman {
			creator = c.from.ID
		}
		id, err := r.db.FindSessionByKey(ctx, "worker", creator, c.key)
		if err != nil {
			t.Fatalf("%s：%v", c.key, err)
		}
		qs, ok := r.db.(store.SessionQueryStore)
		if !ok {
			t.Fatal("测试存储应实现 SessionQueryStore")
		}
		got, err := qs.QuerySessions(ctx, store.SessionQuery{})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, s := range got {
			if s.ID == id {
				found = true
				if s.Origin != c.want {
					t.Fatalf("%s：origin=%q，应为 %q", c.key, s.Origin, c.want)
				}
			}
		}
		if !found {
			t.Fatalf("%s：查不到会话 %s", c.key, id)
		}
	}
}
