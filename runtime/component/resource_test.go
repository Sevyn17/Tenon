package component

import (
	"strings"
	"testing"

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
