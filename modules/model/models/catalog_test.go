package models

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"ybase/runtime/modelhub"
)

func TestBuiltinCatalogResolves(t *testing.T) {
	f, err := BuiltinCatalog()
	if err != nil {
		t.Fatalf("BuiltinCatalog: %v", err)
	}
	c := modelhub.NewCatalog()
	if err := c.Add(modelhub.LayerBuiltin, "builtin", f); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"deepseek", "openai", "anthropic", "openrouter", "ollama"} {
		if _, ok := f.Provider[p]; !ok {
			t.Fatalf("缺内置提供方 %s", p)
		}
	}
	m, p, err := c.Lookup("deepseek-chat")
	if err != nil || p.Protocol != "openai-chat" || m.ContextWindow != 128000 {
		t.Fatalf("deepseek-chat: %+v %+v %v", m, p, err)
	}
	if _, p, _ := c.Lookup("claude-sonnet-4-5"); p.Protocol != "anthropic-messages" {
		t.Fatalf("anthropic 协议: %+v", p)
	}
}

func TestDiscoverOllama(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"models":[{"name":"qwen3:8b"},{"name":"llama3.2"}]}`))
	}))
	defer srv.Close()
	f, err := DiscoverOllama(context.Background(), nil, srv.URL+"/v1")
	if err != nil || len(f.Model) != 2 {
		t.Fatalf("discover: %+v %v", f, err)
	}
	c := modelhub.NewCatalog()
	b, _ := BuiltinCatalog()
	_ = c.Add(modelhub.LayerBuiltin, "builtin", b)
	_ = c.Add(modelhub.LayerDiscover, "ollama", f)
	if _, p, err := c.Lookup("qwen3:8b"); err != nil || p.BaseURL != "http://127.0.0.1:11434/v1" {
		t.Fatalf("发现的模型应挂到内置 ollama 提供方: %+v %v", p, err)
	}
	if _, err := DiscoverOllama(context.Background(), nil, "http://127.0.0.1:1"); err == nil {
		t.Fatal("连不上应返回错误（调用方只告警）")
	}
}
