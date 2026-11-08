package component

import (
	"context"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/runtime/ext"
	"github.com/Sevyn17/tenon/runtime/mail"
)

type fakeSender struct {
	envs []mail.Envelope
	err  error
}

func (f *fakeSender) Deliver(_ context.Context, env mail.Envelope) error {
	if f.err != nil {
		return f.err
	}
	f.envs = append(f.envs, env)
	return nil
}

func TestMailerSendsAsComponent(t *testing.T) {
	sender := &fakeSender{}
	var from mail.Address
	var err error
	name := reg(t, "mailer", &fake{meta: Meta{Effects: []string{EffectTools}},
		setup: func(cx *Registrar) error {
			m := cx.Mailer()
			from = m.From()
			err = m.Send(context.Background(), mail.Address{Kind: mail.AddrSession, ID: "s1"}, "你好")
			return nil
		}})
	if _, aerr := Assemble(Spec{
		Plugins: entries(name),
		Inject:  map[string]any{ext.ServiceHost: sender},
	}); aerr != nil {
		t.Fatalf("Assemble: %v", aerr)
	}
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if from.Kind != mail.AddrComponent || from.ID != name {
		t.Fatalf("From = %v，期望 component:%s", from, name)
	}
	if len(sender.envs) != 1 {
		t.Fatalf("投递了 %d 封，期望 1", len(sender.envs))
	}
	env := sender.envs[0]
	if env.From != from || env.To != (mail.Address{Kind: mail.AddrSession, ID: "s1"}) {
		t.Fatalf("信头 = %v → %v", env.From, env.To)
	}
	if env.Kind != mail.KindRequest || env.Mode != mail.ModeWake || env.Origin != mail.OriginAgent {
		t.Fatalf("kind/mode/origin = %s/%s/%s", env.Kind, env.Mode, env.Origin)
	}
	if env.Body != "你好" || env.ID == "" {
		t.Fatalf("body/id = %q/%q", env.Body, env.ID)
	}
}

func TestMailerWithoutHost(t *testing.T) {
	var err error
	name := reg(t, "nohost", &fake{setup: func(cx *Registrar) error {
		err = cx.Mailer().Send(context.Background(), mail.Address{Kind: mail.AddrSession, ID: "s1"}, "x")
		return nil
	}})
	if _, aerr := Assemble(Spec{Plugins: entries(name)}); aerr != nil {
		t.Fatalf("Assemble: %v", aerr)
	}
	if err == nil || !strings.Contains(err.Error(), "没有内核投递服务") {
		t.Fatalf("err = %v，期望没有投递服务的错误", err)
	}
}
