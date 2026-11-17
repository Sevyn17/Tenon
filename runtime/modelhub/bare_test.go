package modelhub

import (
	"testing"
)

// 裸模型名命中多个时取层级最低的（客户端改进 M3）：内置的 deepseek/deepseek-chat 与插件层同名的
// byok-x/deepseek-chat、gateway/deepseek-chat 并存时，deepseek-chat 仍指内置那个；插件层的要写全。
// 同一层里命中多个仍是歧义（Resolve 报未知），Lookup 取键最小的（结果确定）。
func TestBareNameLowestLayer(t *testing.T) {
	cat := NewCatalog()
	b, _ := ParseCatalog(`
[provider.deepseek]
protocol = "openai-chat"
[model."deepseek/deepseek-chat"]
context_window = 1
`)
	if err := cat.Add(LayerBuiltin, "builtin", b); err != nil {
		t.Fatal(err)
	}
	plug := CatalogFile{
		Provider: map[string]Provider{"byok-a": {Protocol: "openai-chat", Key: "byok:a"}, "gateway": {Protocol: "gw"}},
		Model: map[string]Model{
			"byok-a/deepseek-chat":  {Provider: "byok-a", Name: "deepseek-chat", Label: "日常 · deepseek-chat"},
			"gateway/deepseek-chat": {Provider: "gateway", Name: "deepseek-chat"},
			"byok-a/only-here":      {Provider: "byok-a", Name: "only-here"},
			"gateway/only-here":     {Provider: "gateway", Name: "only-here"},
		},
	}
	if err := cat.Add(LayerPlugin, "plug", plug); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ { // map 遍历顺序随机：多跑几次确认结果确定
		plan, err := cat.Resolve("deepseek-chat")
		if err != nil || len(plan) != 1 || plan[0].Model != "deepseek/deepseek-chat" {
			t.Fatalf("裸名应解析到内置：%+v %v", plan, err)
		}
		if _, p, err := cat.Lookup("deepseek-chat"); err != nil || p.Protocol != "openai-chat" || p.Key != "" {
			t.Fatalf("Lookup 裸名应取内置：%+v %v", p, err)
		}
		if _, err := cat.Resolve("only-here"); err == nil {
			t.Fatal("同一层两个命中应是歧义")
		}
		if m, _, err := cat.Lookup("only-here"); err != nil || m.Provider != "byok-a" {
			t.Fatalf("Lookup 同层歧义应取键最小的：%+v %v", m, err)
		}
	}
	if plan, err := cat.Resolve("byok-a/deepseek-chat"); err != nil || plan[0].Model != "byok-a/deepseek-chat" {
		t.Fatalf("全名照常解析：%+v %v", plan, err)
	}
	var label string
	for _, e := range cat.Entries() {
		if e.ID == "byok-a/deepseek-chat" {
			label = e.Label
		}
	}
	if label != "日常 · deepseek-chat" {
		t.Fatalf("Entries 应带 Label：%q", label)
	}
	if p, ok := cat.Provider("byok-a"); !ok || p.Key != "byok:a" {
		t.Fatalf("Provider：%+v %v", p, ok)
	}
}
