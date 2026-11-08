package models

// 内置提供方与本地发现（model-hub D2 / D3，M3.12）。唯一使用者是本组件，原先放在 runtime/modelhub（2026-11-04 审查 P2 归属）。

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Sevyn17/tenon/runtime/modelhub"
)

//go:embed builtin.toml
var builtinCatalog string

// BuiltinCatalog 内置目录快照（LayerBuiltin）。
func BuiltinCatalog() (modelhub.CatalogFile, error) { return modelhub.ParseCatalog(builtinCatalog) }

// DiscoverOllama 从本地 Ollama 拉取模型列表（目录来源，LayerDiscover）。
// base 为 Ollama 根地址（如 http://127.0.0.1:11434）；失败只返回错误，调用方告警、不阻塞装配。
func DiscoverOllama(ctx context.Context, hc *http.Client, base string) (modelhub.CatalogFile, error) {
	if hc == nil {
		hc = &http.Client{Timeout: 3 * time.Second}
	}
	base = strings.TrimRight(strings.TrimSuffix(strings.TrimRight(base, "/"), "/v1"), "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/tags", nil)
	if err != nil {
		return modelhub.CatalogFile{}, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return modelhub.CatalogFile{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return modelhub.CatalogFile{}, fmt.Errorf("ollama /api/tags: http %d", resp.StatusCode)
	}
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return modelhub.CatalogFile{}, err
	}
	f := modelhub.CatalogFile{Model: map[string]modelhub.Model{}}
	for _, m := range tags.Models {
		if m.Name != "" {
			f.Model["ollama/"+m.Name] = modelhub.Model{Provider: "ollama", Name: m.Name}
		}
	}
	return f, nil
}
