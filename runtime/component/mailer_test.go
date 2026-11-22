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

// SendEnvelope：Key、BudgetRef、Mode 照调用方给的；From/Via 强制本组件；human 来源降为 agent。
func TestMailerSendEnvelope(t *testing.T) {
	sender := &fakeSender{}
	var got mail.Envelope
	var err error
	name := reg(t, "mailenv", &fake{meta: Meta{Effects: []string{EffectTools}},
		setup: func(cx *Registrar) error {
			got, err = cx.Mailer().SendEnvelope(context.Background(), mail.Envelope{
				From:      mail.Address{Kind: mail.AddrHuman, ID: "alice"}, // 伪造：应被改写
				Via:       "other",
				To:        mail.Address{Kind: mail.AddrAgent, ID: "reviewer"},
				Key:       "sess-1",
				BudgetRef: "review:local:2026-11-22",
				Mode:      mail.ModeContext,
				Origin:    mail.OriginHuman, // 伪造：应降为 agent
				Body:      "复盘",
			})
			return nil
		}})
	if _, aerr := Assemble(Spec{Plugins: entries(name), Inject: map[string]any{ext.ServiceHost: sender}}); aerr != nil {
		t.Fatalf("Assemble: %v", aerr)
	}
	if err != nil {
		t.Fatalf("SendEnvelope: %v", err)
	}
	if len(sender.envs) != 1 {
		t.Fatalf("投递了 %d 封，期望 1", len(sender.envs))
	}
	env := sender.envs[0]
	if env.From != (mail.Address{Kind: mail.AddrComponent, ID: name}) || env.Via != name {
		t.Fatalf("from/via = %v/%q，期望 component:%s", env.From, env.Via, name)
	}
	if env.Origin != mail.OriginAgent {
		t.Fatalf("origin = %s，期望 agent", env.Origin)
	}
	if env.Key != "sess-1" || env.BudgetRef != "review:local:2026-11-22" || env.Mode != mail.ModeContext || env.Kind != mail.KindRequest {
		t.Fatalf("key/budget/mode/kind = %q/%q/%s/%s", env.Key, env.BudgetRef, env.Mode, env.Kind)
	}
	if got.ID == "" || got.ID != env.ID {
		t.Fatalf("返回的 id = %q，投递的 id = %q", got.ID, env.ID)
	}
}

// SendEnvelope：external 来源保留（组件可以把外部内容标为不可信）；非法信封报错不投递。
func TestMailerSendEnvelopeOriginAndInvalid(t *testing.T) {
	sender := &fakeSender{}
	var err1, err2 error
	name := reg(t, "mailext", &fake{meta: Meta{Effects: []string{EffectTools}},
		setup: func(cx *Registrar) error {
			_, err1 = cx.Mailer().SendEnvelope(context.Background(), mail.Envelope{
				To: mail.Address{Kind: mail.AddrSession, ID: "s1"}, Origin: mail.OriginExternal, Body: "x"})
			_, err2 = cx.Mailer().SendEnvelope(context.Background(), mail.Envelope{Body: "缺 to"})
			return nil
		}})
	if _, aerr := Assemble(Spec{Plugins: entries(name), Inject: map[string]any{ext.ServiceHost: sender}}); aerr != nil {
		t.Fatalf("Assemble: %v", aerr)
	}
	if err1 != nil || len(sender.envs) != 1 || sender.envs[0].Origin != mail.OriginExternal {
		t.Fatalf("external：err=%v envs=%v", err1, sender.envs)
	}
	if err2 == nil || len(sender.envs) != 1 {
		t.Fatalf("缺 to 应报错且不投递：err=%v 投递数=%d", err2, len(sender.envs))
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
