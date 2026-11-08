// Package models 模型插件 models（标准模块）：模型目录 + modelhub 服务。协议适配器由 adapter-* 插件向 Hub 登记。
package models

import (
	"context"
	"fmt"
	"os"
	"strings"

	"tenon/runtime/assemble"
	"tenon/runtime/component"
	"tenon/runtime/modelhub"
)

// Config models 插件配置。
type Config struct {
	Model   string `cfg:"model" default:"deepseek-chat" title:"模型或别名"`
	Catalog string `cfg:"catalog" title:"用户目录 models.toml" description:"整条覆盖内置目录（model-hub D3）" widget:"file"`
	BaseURL string `cfg:"base_url" title:"临时端点" description:"非空时注册提供方 cli（协议见 protocol，密钥见 api_key）"`
	// APIKey 端点的密钥引用（env:NAME 或 slot:secrets 认得的其他引用），只写引用，不写明文。
	APIKey string `cfg:"api_key" default:"env:TENON_API_KEY" title:"端点密钥引用" description:"如 env:DEEPSEEK_API_KEY；不写明文密钥"`
	// Protocol 端点的协议：openai-chat（缺省）、openai-responses、anthropic-messages。
	Protocol string `cfg:"protocol" default:"openai-chat" title:"端点协议" description:"openai-chat / openai-responses / anthropic-messages"`
	Discover bool   `cfg:"discover_ollama" title:"发现本地 Ollama 模型" widget:"switch"`
}

type modelsPlugin struct{}

func init() { component.Register("models", func() component.Component { return modelsPlugin{} }) }

func (modelsPlugin) Meta() component.Meta {
	return component.Meta{Name: "models", Version: "0.1.0", Default: true, Effects: []string{component.EffectNetwork}, Config: &Config{},
		Requires: []string{assemble.SlotSecrets}, Provides: []string{assemble.ServiceModelhub}}
}

func (modelsPlugin) Setup(cx *component.Registrar) error {
	cfg := cx.Config().(*Config)
	sec, err := component.Use[assemble.Secrets](cx, assemble.SlotSecrets)
	if err != nil {
		return err
	}
	cat := modelhub.NewCatalog()
	bf, err := BuiltinCatalog()
	if err != nil {
		return err
	}
	if err := cat.Add(modelhub.LayerBuiltin, "builtin", bf); err != nil {
		return err
	}
	if cfg.Discover {
		if f, err := DiscoverOllama(context.Background(), nil, "http://127.0.0.1:11434"); err == nil {
			_ = cat.Add(modelhub.LayerDiscover, "ollama", f)
		} // 失败只是没有本地模型，不阻塞装配
	}
	if cfg.Catalog != "" {
		b, err := os.ReadFile(cfg.Catalog)
		if err != nil {
			return fmt.Errorf("models: 读目录 %s: %w", cfg.Catalog, err)
		}
		f, err := modelhub.ParseCatalog(string(b))
		if err != nil {
			return err
		}
		if err := cat.Add(modelhub.LayerUser, cfg.Catalog, f); err != nil {
			return err
		}
	}
	model := cfg.Model
	if cfg.BaseURL != "" {
		switch cfg.Protocol {
		case "openai-chat", "openai-responses", "anthropic-messages":
		default:
			return fmt.Errorf("models: protocol %q 不认识（openai-chat / openai-responses / anthropic-messages）", cfg.Protocol)
		}
		if strings.TrimSpace(cfg.APIKey) != "" && !strings.Contains(cfg.APIKey, ":") {
			return fmt.Errorf("models: api_key 要写成引用（如 env:NAME），不写明文")
		}
		f := modelhub.CatalogFile{
			Provider: map[string]modelhub.Provider{"cli": {Protocol: cfg.Protocol, BaseURL: cfg.BaseURL, Key: cfg.APIKey}},
			Model:    map[string]modelhub.Model{"cli/" + model: {Provider: "cli", Name: model}},
		}
		if err := cat.Add(modelhub.LayerUser, "cli", f); err != nil {
			return err
		}
		model = "cli/" + model
	}
	if _, err := cat.Resolve(model); err != nil {
		return err
	}
	return cx.Provide("modelhub", &assemble.Models{Hub: modelhub.New(cat, sec.Resolve, nil), Model: model, Secrets: sec})
}

func (modelsPlugin) Start(*component.Scope) error { return nil }
