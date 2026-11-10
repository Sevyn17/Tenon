package component

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/runtime/ext"
	"github.com/Sevyn17/tenon/runtime/mail"
)

func TestResourceTypeAndMatcherRegistration(t *testing.T) {
	var regErr, dupType, dupMatcher, badName, nilMatcher error
	name := reg(t, "board", &fake{setup: func(cx *Registrar) error {
		regErr = cx.ResourceType(ResourceType{Name: "board"})
		dupType = cx.ResourceType(ResourceType{Name: "board"})
		badName = cx.ResourceType(ResourceType{Name: "Board"})
		if err := cx.Matcher("mentions", func(ResourceRecord, map[string]string) (Match, error) {
			return Match{Matched: true, Mode: mail.ModeWake}, nil
		}); err != nil {
			regErr = err
		}
		dupMatcher = cx.Matcher(MatcherExact, func(ResourceRecord, map[string]string) (Match, error) {
			return Match{}, nil
		})
		nilMatcher = cx.Matcher("empty", nil)
		return nil
	}})
	other := reg(t, "other", &fake{setup: func(cx *Registrar) error {
		return cx.ResourceType(ResourceType{Name: "board"})
	}})
	if _, err := Assemble(Spec{Plugins: entries(name, other)}); err == nil || !strings.Contains(err.Error(), "重复登记") {
		t.Fatalf("跨组件重复登记资源类型应拒绝：%v", err)
	}
	a, err := Assemble(Spec{Plugins: entries(name)})
	if err != nil {
		t.Fatal(err)
	}
	if regErr != nil {
		t.Fatal(regErr)
	}
	if dupType == nil || !strings.Contains(dupType.Error(), "重复登记") {
		t.Fatalf("重复资源类型 = %v", dupType)
	}
	if badName == nil || !strings.Contains(badName.Error(), "不合法") {
		t.Fatalf("非法资源类型名 = %v", badName)
	}
	if dupMatcher == nil || !strings.Contains(dupMatcher.Error(), "重复登记") {
		t.Fatalf("与内置重名应拒绝：%v", dupMatcher)
	}
	if nilMatcher == nil || !strings.Contains(nilMatcher.Error(), "缺函数") {
		t.Fatalf("空匹配器 = %v", nilMatcher)
	}
	got := a.ResourceTypes()
	if len(got) != 1 || got[0].Name != "board" {
		t.Fatalf("资源类型 = %#v", got)
	}
	if _, ok := a.Matcher("mentions"); !ok {
		t.Fatal("登记的匹配器应能取到")
	}
	for _, builtin := range []string{MatcherExact, MatcherPrefix, MatcherList} {
		if _, ok := a.Matcher(builtin); !ok {
			t.Fatalf("内置匹配器 %s 缺失", builtin)
		}
	}
}

func TestBuiltinMatchers(t *testing.T) {
	rec := ResourceRecord{Fields: map[string]string{"body": "@alice 看一下", "from": "human:u1"}}
	exact, _ := matchExact(rec, map[string]string{"field": "from", "value": "human:u1"})
	if !exact.Matched {
		t.Fatal("精确匹配应命中")
	}
	miss, _ := matchExact(rec, map[string]string{"field": "from", "value": "human:u2"})
	if miss.Matched {
		t.Fatal("精确匹配不应命中别的值")
	}
	absent, _ := matchExact(rec, map[string]string{"field": "tag", "value": "x"})
	if absent.Matched {
		t.Fatal("字段不存在应视为不命中，不是错误")
	}
	prefix, _ := matchPrefix(rec, map[string]string{"field": "body", "value": "@alice"})
	if !prefix.Matched {
		t.Fatal("前缀匹配应命中")
	}
	list, _ := matchList(rec, map[string]string{"field": "from", "value": "human:u2,human:u1"})
	if !list.Matched {
		t.Fatal("名单匹配应命中其中一项")
	}
	if _, err := matchExact(rec, map[string]string{"field": "from"}); err == nil {
		t.Fatal("缺 value 应返回错误")
	}
	if _, err := matchList(rec, map[string]string{}); err == nil {
		t.Fatal("缺 field 与 value 应返回错误")
	}
}

func TestFanoutDeliversOnlyMatches(t *testing.T) {
	sender := &fakeSender{}
	var result FanoutResult
	var ferr error
	name := reg(t, "board", &fake{setup: func(cx *Registrar) error {
		rec := ResourceRecord{
			Resource: "group:g1", Seq: 3,
			From: mail.Address{Kind: mail.AddrHuman, ID: "u1"},
			Body: "hello",
		}
		subs := []Subscription{
			{ID: "hit", Subscriber: "session:s1", Matcher: MatcherExact,
				Params: map[string]string{"field": "from", "value": "human:u1"}},
			{ID: "miss", Subscriber: "session:s2", Matcher: MatcherExact,
				Params: map[string]string{"field": "from", "value": "human:u2"}},
			{ID: "self", Subscriber: "session:s3", Matcher: MatcherExact,
				Params: map[string]string{"field": "from", "value": "human:u1"}, Exclude: "human:u1,human:u9"},
			{ID: "bad", Subscriber: "human:u3", Matcher: MatcherExact,
				Params: map[string]string{"field": "from", "value": "human:u1"}},
		}
		result, ferr = cx.Mailer().Fanout(context.Background(), rec, subs)
		return nil
	}})
	if _, err := Assemble(Spec{Plugins: entries(name), Inject: map[string]any{ext.ServiceHost: sender}}); err != nil {
		t.Fatal(err)
	}
	if ferr != nil {
		t.Fatal(ferr)
	}
	if result.Delivered != 1 || result.Skipped != 2 || len(result.Errors) != 1 {
		t.Fatalf("结果 = %+v，期望投出 1、跳过 2、错误 1", result)
	}
	if !strings.Contains(result.Errors[0], "bad:") || !strings.Contains(result.Errors[0], "session:") {
		t.Fatalf("坏地址应记入错误：%v", result.Errors)
	}
	if len(sender.envs) != 1 {
		t.Fatalf("投出 %d 封", len(sender.envs))
	}
	env := sender.envs[0]
	if env.From != (mail.Address{Kind: mail.AddrHuman, ID: "u1"}) || env.Via != name {
		t.Fatalf("from/via = %s/%s", env.From, env.Via)
	}
	if env.To != (mail.Address{Kind: mail.AddrSession, ID: "s1"}) {
		t.Fatalf("to = %s", env.To)
	}
	if env.Kind != mail.KindNotify || env.Mode != mail.ModeNotice || env.Origin != mail.OriginExternal {
		t.Fatalf("kind/mode/origin = %s/%s/%s", env.Kind, env.Mode, env.Origin)
	}
	if env.Body != "hello" || env.Hop != 0 || env.Trace != "" || env.Caps != nil {
		t.Fatalf("body/hop/trace/caps = %q/%d/%q/%v", env.Body, env.Hop, env.Trace, env.Caps)
	}
}

func TestFanoutWithoutHost(t *testing.T) {
	var ferr error
	name := reg(t, "nohost", &fake{setup: func(cx *Registrar) error {
		_, ferr = cx.Mailer().Fanout(context.Background(), ResourceRecord{Resource: "group:g1"}, nil)
		return nil
	}})
	if _, err := Assemble(Spec{Plugins: entries(name)}); err != nil {
		t.Fatal(err)
	}
	if ferr == nil || !strings.Contains(ferr.Error(), "没有内核投递服务") {
		t.Fatalf("没有投递服务应返回错误：%v", ferr)
	}
}

func TestFanoutCollectsMatcherAndDeliveryErrors(t *testing.T) {
	sender := &fakeSender{err: errors.New("联系名单拒绝")}
	var result FanoutResult
	name := reg(t, "board", &fake{setup: func(cx *Registrar) error {
		rec := ResourceRecord{Resource: "group:g1", Seq: 1, From: mail.Address{Kind: mail.AddrHuman, ID: "u1"}, Body: "x"}
		subs := []Subscription{
			{ID: "unknown", Subscriber: "session:s1", Matcher: "no_such"},
			{ID: "badparams", Subscriber: "session:s2", Matcher: MatcherExact, Params: map[string]string{"field": "from"}},
			{ID: "denied", Subscriber: "session:s3", Matcher: MatcherExact,
				Params: map[string]string{"field": "body", "value": "x"}},
		}
		var err error
		result, err = cx.Mailer().Fanout(context.Background(), rec, subs)
		if err != nil {
			t.Fatalf("单条失败不应让 Fanout 返回错误：%v", err)
		}
		return nil
	}})
	if _, err := Assemble(Spec{Plugins: entries(name), Inject: map[string]any{ext.ServiceHost: sender}}); err != nil {
		t.Fatal(err)
	}
	if len(result.Denied) != 1 || result.Denied[0] != "denied" {
		t.Fatalf("被拒 = %v", result.Denied)
	}
	if len(result.Errors) != 2 {
		t.Fatalf("错误 = %v，期望 2 条", result.Errors)
	}
	if len(sender.envs) != 0 {
		t.Fatalf("被拒的不应留下信封：%d", len(sender.envs))
	}
}

func TestFanoutUsesMatcherModeAndRender(t *testing.T) {
	sender := &fakeSender{}
	name := reg(t, "board", &fake{setup: func(cx *Registrar) error {
		if err := cx.ResourceType(ResourceType{Name: "group", Render: func(rec ResourceRecord) string {
			return "渲染:" + rec.Body
		}}); err != nil {
			return err
		}
		if err := cx.Matcher("wake_on", func(ResourceRecord, map[string]string) (Match, error) {
			return Match{Matched: true, Mode: mail.ModeWake}, nil
		}); err != nil {
			return err
		}
		rec := ResourceRecord{Resource: "group:g1", Seq: 1, From: mail.Address{Kind: mail.AddrHuman, ID: "u1"}, Body: "原文"}
		_, err := cx.Mailer().Fanout(context.Background(), rec, []Subscription{
			{ID: "s", Subscriber: "session:s1", Matcher: "wake_on"},
		})
		return err
	}})
	if _, err := Assemble(Spec{Plugins: entries(name), Inject: map[string]any{ext.ServiceHost: sender}}); err != nil {
		t.Fatal(err)
	}
	if len(sender.envs) != 1 {
		t.Fatalf("投出 %d 封", len(sender.envs))
	}
	env := sender.envs[0]
	if env.Mode != mail.ModeWake || env.Body != "渲染:原文" {
		t.Fatalf("mode/body = %s/%q", env.Mode, env.Body)
	}
}

// TestFanoutTrimsListWhitespace：exclude 与 list 的逗号分隔项去掉首尾空白。
func TestFanoutTrimsListWhitespace(t *testing.T) {
	sender := &fakeSender{}
	var result FanoutResult
	name := reg(t, "board", &fake{setup: func(cx *Registrar) error {
		rec := ResourceRecord{Resource: "group:g1", Seq: 1,
			From: mail.Address{Kind: mail.AddrHuman, ID: "u1"}, Body: "x",
			Fields: map[string]string{"tag": "beta"}}
		subs := []Subscription{
			{ID: "spaced", Subscriber: "session:s1", Matcher: MatcherList,
				Params: map[string]string{"field": "tag", "value": "alpha, beta, gamma"}},
			{ID: "excluded", Subscriber: "session:s2", Matcher: MatcherList,
				Params: map[string]string{"field": "tag", "value": "beta"}, Exclude: "human:u9, human:u1"},
		}
		var err error
		result, err = cx.Mailer().Fanout(context.Background(), rec, subs)
		return err
	}})
	if _, err := Assemble(Spec{Plugins: entries(name), Inject: map[string]any{ext.ServiceHost: sender}}); err != nil {
		t.Fatal(err)
	}
	if result.Delivered != 1 || result.Skipped != 1 || len(result.Errors) != 0 {
		t.Fatalf("带空格的名单应正常命中与排除：%+v", result)
	}
	if len(sender.envs) != 1 || sender.envs[0].To.ID != "s1" {
		t.Fatalf("应只投给 s1：%v", sender.envs)
	}
}

// TestFanoutRejectsBadMatcherMode：匹配器给了非法 mode 记进 Errors，不投出。
func TestFanoutRejectsBadMatcherMode(t *testing.T) {
	sender := &fakeSender{}
	var result FanoutResult
	name := reg(t, "board", &fake{setup: func(cx *Registrar) error {
		if err := cx.Matcher("bad_mode", func(ResourceRecord, map[string]string) (Match, error) {
			return Match{Matched: true, Mode: mail.Mode("nope")}, nil
		}); err != nil {
			return err
		}
		rec := ResourceRecord{Resource: "group:g1", Seq: 1, From: mail.Address{Kind: mail.AddrHuman, ID: "u1"}, Body: "x"}
		var err error
		result, err = cx.Mailer().Fanout(context.Background(), rec, []Subscription{
			{ID: "bad", Subscriber: "session:s1", Matcher: "bad_mode"},
		})
		return err
	}})
	if _, err := Assemble(Spec{Plugins: entries(name), Inject: map[string]any{ext.ServiceHost: sender}}); err != nil {
		t.Fatal(err)
	}
	if len(result.Errors) != 1 || !strings.Contains(result.Errors[0], "mode") || result.Delivered != 0 {
		t.Fatalf("非法 mode 应记入错误：%+v", result)
	}
	if len(sender.envs) != 0 {
		t.Fatalf("非法 mode 不应投出：%d", len(sender.envs))
	}
}
