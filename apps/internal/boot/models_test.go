package boot

import (
	"testing"

	"ybase/runtime/assemble"
	"ybase/runtime/modelhub"
)

func TestModelListAvailability(t *testing.T) {
	t.Setenv("ZZ_KEY_OK", "x")
	cat := modelhub.NewCatalog()
	on := true
	if err := cat.Add(modelhub.LayerUser, "t", modelhub.CatalogFile{
		Provider: map[string]modelhub.Provider{
			"ok":    {Protocol: "openai-chat", Key: "env:ZZ_KEY_OK"},
			"miss":  {Protocol: "openai-chat", Key: "env:ZZ_KEY_MISSING_FOR_TEST"},
			"local": {Protocol: "openai-chat"},
		},
		Model: map[string]modelhub.Model{
			"ok/a":    {Provider: "ok", ContextWindow: 1000, Caps: modelhub.Capabilities{Images: &on}},
			"miss/b":  {Provider: "miss"},
			"local/c": {Provider: "local"},
		},
		Alias: map[string]struct {
			Plan modelhub.Plan `toml:"plan"`
		}{"fast": {Plan: modelhub.Plan{{Model: "ok/a"}}}},
	}); err != nil {
		t.Fatal(err)
	}
	l := ModelList{M: &assemble.Models{Hub: modelhub.New(cat, assemble.EnvSecrets{}.Resolve, nil), Model: "fast", Secrets: assemble.EnvSecrets{}}}
	r := l.List()
	if r.Default != "fast" || len(r.Models) != 4 || r.Models[0].ID != "fast" || !r.Models[0].Alias || r.Models[0].Provider != "ok" {
		t.Fatalf("列表：%+v", r)
	}
	got := map[string]bool{}
	for _, m := range r.Models {
		got[m.ID] = m.Available
		if m.ID == "ok/a" && (!m.Images || m.ContextWindow != 1000 || m.Name != "a") {
			t.Fatalf("ok/a：%+v", m)
		}
		if m.ID == "miss/b" && m.Reason == "" {
			t.Fatal("不可用应带原因")
		}
	}
	if !got["ok/a"] || got["miss/b"] || !got["local/c"] {
		t.Fatalf("可用性：%v", got)
	}
	if l.Check("fast") != nil || l.Check("a") != nil || l.Check("nope") == nil {
		t.Fatal("Check")
	}
}
