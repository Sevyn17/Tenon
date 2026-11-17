// Package models 模型插件 models（标准模块）：模型目录 + modelhub 服务。协议适配器由 adapter-* 插件向 Hub 登记。
//
// 缺省模型怎么定（客户端改进 M3，配置T3）：
//   - 没有组件填 slot:model_sources（框架自己的装配都是这样）：和以前完全一样，Setup 里定下 cfg.Model
//     （缺省 deepseek-chat），解析不了装配失败。
//   - 填了：插槽给的插件层目录（如 BYOK）在 Setup 里合入目录；缺省模型挪到 Start 再定——这时所有组件都
//     Setup 完了，别的组件（如平台网关）注册的模型也在目录里。顺序：配置里写了 model / base_url → 用它；
//     没写 → 插槽的 DefaultModel；再空 → deepseek-chat。配置写的模型解析不了时不让装配失败，回落到后两项并
//     在 Models.Notice 里说明（桌面端起不来比用错模型更糟）。
package models

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/Sevyn17/tenon/runtime/appid"
	"github.com/Sevyn17/tenon/runtime/assemble"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/modelhub"
)

// DefaultModel 内置缺省模型（Config.Model 的 default 标签与它一致）。
const DefaultModel = "deepseek-chat"

// Config models 插件配置。
type Config struct {
	Model   string `cfg:"model" default:"deepseek-chat" title:"模型或别名"`
	Catalog string `cfg:"catalog" title:"用户目录 models.toml" description:"整条覆盖内置目录（model-hub D3）" widget:"file"`
	BaseURL string `cfg:"base_url" title:"临时端点" description:"非空时注册提供方 cli（协议见 protocol，密钥见 api_key）"`
	// APIKey 端点的密钥引用（env:NAME 或 slot:secrets 认得的其他引用），只写引用，不写明文。
	APIKey string `cfg:"api_key" title:"端点密钥引用" description:"如 env:DEEPSEEK_API_KEY；空 = env:<前缀>_API_KEY（缺省 TENON_API_KEY）；不写明文密钥"`
	// Protocol 端点的协议：openai-chat（缺省）、openai-responses、anthropic-messages。
	Protocol string `cfg:"protocol" default:"openai-chat" title:"端点协议" description:"openai-chat / openai-responses / anthropic-messages"`
	Discover bool   `cfg:"discover_ollama" title:"发现本地 Ollama 模型" widget:"switch"`
}

// modelsPlugin 每次装配一个新实例（注册表的工厂函数）：Setup 记下的状态只给本次装配的 Start 用。
type modelsPlugin struct {
	src      assemble.ModelSources // 填了 slot:model_sources 时非空
	models   *assemble.Models
	explicit string // 配置里写明的模型（含 cli/ 前缀）；"" = 没写
}

func init() { component.Register("models", func() component.Component { return &modelsPlugin{} }) }

func (*modelsPlugin) Meta() component.Meta {
	return component.Meta{Name: "models", Version: "0.1.0", Default: true, Effects: []string{component.EffectNetwork}, Config: &Config{},
		Requires: []string{assemble.SlotSecrets, component.Optional + assemble.SlotModelSources}, Provides: []string{assemble.ServiceModelhub}}
}

func (p *modelsPlugin) Setup(cx *component.Registrar) error {
	cfg := cx.Config().(*Config)
	sec, err := component.Use[assemble.Secrets](cx, assemble.SlotSecrets)
	if err != nil {
		return err
	}
	src, hasSrc, err := component.UseOptional[assemble.ModelSources](cx, assemble.SlotModelSources)
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
	if hasSrc { // 插件层：低于本地发现与用户 models.toml，用户仍可整条覆盖
		if err := cat.Add(modelhub.LayerPlugin, "model_sources", src.Catalog()); err != nil {
			return err
		}
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
	explicit := cx.ConfigSource("model") == "profile"
	if cfg.BaseURL != "" {
		if strings.TrimSpace(cfg.APIKey) == "" {
			cfg.APIKey = "env:" + appid.EnvName("API_KEY")
		}
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
		explicit = true // 给了端点就是指定了模型（cli/<model>）
	}
	origin := assemble.OriginBuiltin
	if explicit {
		origin = assemble.OriginConfig
	}
	hub := modelhub.New(cat, sec.Resolve, nil)
	if !hasSrc { // 没有模型来源插槽：与以前一样当场定下，解析不了装配失败
		if _, err := cat.Resolve(model); err != nil {
			return err
		}
		return cx.Provide("modelhub", &assemble.Models{Hub: hub, Model: model, Secrets: sec, Origin: origin})
	}
	// 有插槽：先给出暂定值（Setup 期就读 Model 的组件拿到配置值；compact-basic 在自己的 Start 里按定下的值重算），
	// 缺省模型到 Start 再定。
	p.src, p.explicit = src, ""
	if explicit {
		p.explicit = model
	}
	p.models = &assemble.Models{Hub: hub, Model: model, Secrets: sec, Origin: origin}
	return cx.Provide("modelhub", p.models)
}

// Start 有模型来源插槽时定下缺省模型（见包注释）；没有插槽时什么也不做。
func (p *modelsPlugin) Start(*component.Scope) error {
	if p.src == nil || p.models == nil {
		return nil
	}
	return p.decide()
}

// decide 按「配置 > 插槽自动 > 内置」定缺省模型。只在内置模型也解析不了时报错。
func (p *modelsPlugin) decide() error {
	m := p.models
	cat := m.Hub.Catalog()
	m.Notice, m.OriginRef = "", ""
	if p.explicit != "" {
		if _, err := cat.Resolve(p.explicit); err == nil {
			m.Model, m.Origin = p.explicit, assemble.OriginConfig
			return nil
		}
	}
	auto, why := p.src.DefaultModel(cat)
	if auto != "" {
		if _, err := cat.Resolve(auto); err != nil {
			auto = ""
		}
	}
	if auto != "" {
		m.Model, m.Origin, m.OriginRef = auto, assemble.OriginAuto, why
	} else {
		if _, err := cat.Resolve(DefaultModel); err != nil {
			return err
		}
		m.Model, m.Origin = DefaultModel, assemble.OriginBuiltin
	}
	if p.explicit != "" {
		m.Notice = fmt.Sprintf("配置的缺省模型 %s 在目录里找不到（对应的配置可能已删除），这次改用 %s", p.explicit, m.Model)
	}
	return nil
}
