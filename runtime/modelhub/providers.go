package modelhub

// 内置提供方与本地发现（model-hub D2 / D3，M3.12）。

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

//go:embed builtin.toml
var builtinCatalog string

// BuiltinCatalog 内置目录快照（LayerBuiltin）。
func BuiltinCatalog() (CatalogFile, error) { return ParseCatalog(builtinCatalog) }

// DiscoverOllama 从本地 Ollama 拉取模型列表（目录来源，LayerDiscover）。
// base 为 Ollama 根地址（如 http://127.0.0.1:11434）；失败只返回错误，调用方告警、不阻塞装配。
func DiscoverOllama(ctx context.Context, hc *http.Client, base string) (CatalogFile, error) {
	if hc == nil {
		hc = &http.Client{Timeout: 3 * time.Second}
	}
	base = strings.TrimRight(strings.TrimSuffix(strings.TrimRight(base, "/"), "/v1"), "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/tags", nil)
	if err != nil {
		return CatalogFile{}, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return CatalogFile{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return CatalogFile{}, fmt.Errorf("ollama /api/tags: http %d", resp.StatusCode)
	}
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return CatalogFile{}, err
	}
	f := CatalogFile{Model: map[string]Model{}}
	for _, m := range tags.Models {
		if m.Name != "" {
			f.Model["ollama/"+m.Name] = Model{Provider: "ollama", Name: m.Name}
		}
	}
	return f, nil
}
