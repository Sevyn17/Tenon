package adapters

import (
	"testing"

	"github.com/Sevyn17/tenon/runtime/assemble"
	"github.com/Sevyn17/tenon/runtime/modelhub"
)

// describer 假的产品凭据说明：byok: 是「我的模型」，mortise-gateway 协议是平台账号（未登录 = 不可用）。
type describer struct{ loggedIn bool }

func (describer) Resolve(ref string) (string, error) { return "k", nil }

func (d describer) DescribeCredential(protocol, key string) (string, string, string, bool) {
	switch {
	case protocol == "mortise-gateway":
		if !d.loggedIn {
			return "platform", "平台账号", "未登录平台账号", true
		}
		return "platform", "平台账号", "", true
	case len(key) > 5 && key[:5] == "byok:":
		return "byok", "我的模型：日常", "", true
	}
	return "", "", "", false
}

func effCatalog(t *testing.T) *modelhub.Catalog {
	t.Helper()
	cat := modelhub.NewCatalog()
	if err := cat.Add(modelhub.LayerPlugin, "t", modelhub.CatalogFile{
		Provider: map[string]modelhub.Provider{
			"envp":    {Protocol: "openai-chat", BaseURL: "https://env", Key: "env:ZZ_EFF_KEY"},
			"byok-1":  {Protocol: "openai-chat", BaseURL: "https://byok", Key: "byok:1"},
			"gateway": {Protocol: "mortise-gateway"},
		},
		Model: map[string]modelhub.Model{
			"envp/a":          {Provider: "envp"},
			"byok-1/b":        {Provider: "byok-1", Name: "b", Label: "日常 · b"},
			"gateway/default": {Provider: "gateway", Name: "default"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	return cat
}

// effective：环境变量凭据（缺省规则）、BYOK 与平台（Secrets 说明凭据）、目录里没有的缺省模型。
func TestModelListEffective(t *testing.T) {
	cat := effCatalog(t)
	hub := modelhub.New(cat, func(string) (string, error) { return "", nil }, nil)

	t.Setenv("ZZ_EFF_KEY", "")
	l := ModelList{M: &assemble.Models{Hub: hub, Model: "envp/a", Secrets: assemble.EnvSecrets{}, Origin: "env", OriginRef: "MORTISE_MODEL"}}
	ef := l.List().Effective
	if ef == nil || ef.Provider != "envp" || ef.BaseURL != "https://env" || ef.CredKind != "env" ||
		ef.CredLabel != "环境变量 ZZ_EFF_KEY" || ef.Available || ef.Reason == "" || ef.Origin != "env" || ef.OriginRef != "MORTISE_MODEL" {
		t.Fatalf("环境变量没设：%+v", ef)
	}
	t.Setenv("ZZ_EFF_KEY", "x")
	if ef := l.List().Effective; !ef.Available || ef.Reason != "" {
		t.Fatalf("环境变量设了应可用：%+v", ef)
	}

	l = ModelList{M: &assemble.Models{Hub: hub, Model: "byok-1/b", Secrets: describer{}, Origin: assemble.OriginAuto, OriginRef: "自动：我的模型"}}
	r := l.List()
	if ef := r.Effective; ef.CredKind != "byok" || ef.CredLabel != "我的模型：日常" || !ef.Available || ef.Label != "日常 · b" || ef.Origin != "auto" {
		t.Fatalf("BYOK：%+v", ef)
	}
	for _, m := range r.Models {
		if m.ID == "byok-1/b" && m.Label != "日常 · b" {
			t.Fatalf("ModelInfo.label：%+v", m)
		}
		if m.ID == "gateway/default" && (m.Available || m.Reason != "未登录平台账号") {
			t.Fatalf("未登录时网关模型应不可用：%+v", m)
		}
	}

	l = ModelList{M: &assemble.Models{Hub: hub, Model: "gateway/default", Secrets: describer{loggedIn: true}}}
	if ef := l.List().Effective; ef.CredKind != "platform" || ef.Protocol != "mortise-gateway" || !ef.Available {
		t.Fatalf("平台：%+v", ef)
	}

	l = ModelList{M: &assemble.Models{Hub: hub, Model: "nope", Secrets: describer{}, Notice: "回落了"}}
	if ef := l.List().Effective; ef.Available || ef.Reason == "" || ef.Notice != "回落了" {
		t.Fatalf("目录里没有的缺省模型：%+v", ef)
	}
}
