package host

// 资源扇出的守卫验收（04 4.7，T4c 第 3 步）。
//
// 用真实 Host.Deliver：两条订阅、一个精确匹配器，只有匹配的订阅者收到信封。
// 不登记放行规则时，匹配的订阅者也收不到，拒绝记 guard.denied。
// 登记一条只放行"本组件转发、via 为本组件"的 Allow 规则后才收得到。

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/Sevyn17/tenon/runtime/agent"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/ext"
	"github.com/Sevyn17/tenon/runtime/mail"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
)

// fanoutPlug 验收用组件：登记资源类型，Setup 时把发信句柄交出来。
type fanoutPlug struct {
	mailer *component.Mailer
}

func (p *fanoutPlug) Meta() component.Meta { return component.Meta{Name: "board"} }
func (p *fanoutPlug) Setup(cx *component.Registrar) error {
	if err := cx.ResourceType(component.ResourceType{Name: "group"}); err != nil {
		return err
	}
	// 只放行"本组件转发、via 为本组件"的信封。名字缺省时记成本组件名。
	cx.Guard(component.GuardRule{On: component.GuardDeliver, Check: func(req component.GuardRequest) (component.Verdict, string) {
		env := req.Envelope
		if env != nil && env.Via == cx.Plugin() && env.Origin == mail.OriginExternal {
			return component.Allow, ""
		}
		return component.Abstain, ""
	}})
	p.mailer = cx.Mailer()
	return nil
}
func (p *fanoutPlug) Start(*component.Scope) error { return nil }

// openAs 建一个指定创建者的会话。
func openAs(t *testing.T, h *Host, id, creator string) {
	t.Helper()
	if _, err := h.open(context.Background(), id, "", h.New, func(ag *agent.Agent, cfg *session.Config) error {
		_, err := ag.OpenAs(context.Background(), cfg, store.SessionMeta{ID: id, Creator: creator})
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// inboxOf 读一个会话信箱里的信封。
func inboxOf(t *testing.T, db interface {
	Inbox(context.Context, string) ([]store.InboxItem, error)
}, id string) []mail.Envelope {
	t.Helper()
	items, err := db.Inbox(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var out []mail.Envelope
	for _, it := range items {
		env, ok := parseEnvelope(it)
		if !ok {
			t.Fatalf("信箱项 %s 没有信封", it.ItemID)
		}
		out = append(out, env)
	}
	return out
}

// TestFanoutAcceptance 注册一个资源类型、两条订阅、一个精确匹配器：
// 记录进来后只有匹配的订阅者收到信封；不登记放行规则时谁都收不到，拒绝记 guard.denied。
func TestFanoutAcceptance(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	openAs(t, r.h, "s1", "u1")
	openAs(t, r.h, "s2", "u1")

	plug := &fanoutPlug{}
	asm, err := component.Assemble(component.Spec{
		Plugins:   []component.Entry{{Name: "board"}},
		Instances: map[string]component.Component{"board": plug},
		Inject:    map[string]any{ext.ServiceHost: r.h},
	})
	if err != nil {
		t.Fatal(err)
	}
	// 先不接放行规则：u2 不是创建者，匹配的订阅者也收不到。
	r.h.Guards = nil

	rec := component.ResourceRecord{
		Resource: "group:g1", Seq: 1,
		From: mail.Address{Kind: mail.AddrHuman, ID: "u2"},
		Body: "大家好",
	}
	subs := []component.Subscription{
		{ID: "hit", Subscriber: "session:s1", Matcher: component.MatcherExact,
			Params: map[string]string{"field": "from", "value": "human:u2"}},
		{ID: "miss", Subscriber: "session:s2", Matcher: component.MatcherExact,
			Params: map[string]string{"field": "from", "value": "human:u9"}},
	}

	// 不登记放行规则：u2 不是创建者，匹配的订阅者也收不到。
	result, err := plug.mailer.Fanout(ctx, rec, subs)
	if err != nil {
		t.Fatal(err)
	}
	if result.Delivered != 0 || len(result.Denied) != 1 || result.Denied[0] != "hit" || result.Skipped != 1 {
		t.Fatalf("缺放行规则时的结果 = %+v", result)
	}
	if got := inboxOf(t, r.db, "s1"); len(got) != 0 {
		t.Fatalf("被拒的不应入信箱：%+v", got)
	}
	// 只数 guard.denied（其它组件事件与本次断言无关，避免被无关事件干扰）。
	var deniedEvents []store.ComponentEvent
	if all, err := r.db.Read(ctx, 0, 0); err != nil {
		t.Fatalf("读组件事件：%v", err)
	} else {
		for _, e := range all {
			if e.Name == "guard.denied" {
				deniedEvents = append(deniedEvents, e)
			}
		}
	}
	if len(deniedEvents) != 1 {
		t.Fatalf("应记一条 guard.denied，实际 %d 条（全部事件 %+v）", len(deniedEvents), deniedEvents)
	}
	events := deniedEvents
	var data map[string]string
	if err := json.Unmarshal(events[0].Data, &data); err != nil {
		t.Fatal(err)
	}
	if data["from"] != "human:u2" || data["to"] != "session:s1" {
		t.Fatalf("拒绝载荷不对：%v", data)
	}

	// 接上组件经 Registrar.Guard 登记的规则，再投一次。
	rules := asm.GuardRules()
	if len(rules) != 1 || rules[0].Name != "board" {
		t.Fatalf("规则应记成本组件名：%+v", rules)
	}
	r.h.Guards = rules
	rec.Seq = 2
	result, err = plug.mailer.Fanout(ctx, rec, subs)
	if err != nil {
		t.Fatal(err)
	}
	if result.Delivered != 1 || result.Skipped != 1 || len(result.Denied) != 0 {
		t.Fatalf("放行后的结果 = %+v", result)
	}
	got := inboxOf(t, r.db, "s1")
	if len(got) != 1 {
		t.Fatalf("匹配的订阅者应收到一封：%+v", got)
	}
	env := got[0]
	if env.From.String() != "human:u2" || env.Via != "board" || env.To.String() != "session:s1" {
		t.Fatalf("信头 = %s via %s -> %s", env.From, env.Via, env.To)
	}
	if env.Kind != mail.KindNotify || env.Mode != mail.ModeNotice || env.Origin != mail.OriginExternal {
		t.Fatalf("kind/mode/origin = %s/%s/%s", env.Kind, env.Mode, env.Origin)
	}
	if env.Hop != 1 || env.Trace == "" {
		t.Fatalf("内核应写 hop=1 与 trace，得 hop=%d trace=%q", env.Hop, env.Trace)
	}
	if env.Body != "大家好" {
		t.Fatalf("正文 = %q", env.Body)
	}
	if missed := inboxOf(t, r.db, "s2"); len(missed) != 0 {
		t.Fatalf("不匹配的订阅者不应收到：%+v", missed)
	}
	if again, err := r.db.Read(ctx, 0, 0); err != nil || len(again) != 1 {
		t.Fatalf("放行后不应再记拒绝：%v %+v", err, again)
	}
}
