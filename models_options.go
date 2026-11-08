package tenon

// 模型与凭证的外观（计划/08 3.2）：类型化地配 models 组件，代替 WithConfig("models", map{…})。
// 密钥只以引用出现（model-hub D4）：Env("NAME") 读环境变量；接自己的密钥库用 WithSecrets。

import (
	"fmt"
	"strings"

	"github.com/Sevyn17/tenon/runtime/assemble"
)

// KeyRef 密钥引用，如 env:DEEPSEEK_API_KEY。不是密钥本身。
type KeyRef string

// Env 从环境变量 name 读密钥。
func Env(name string) KeyRef { return KeyRef("env:" + name) }

// WithOpenAI 用一个 OpenAI 兼容端点（OpenAI、DeepSeek、通义、本地 vLLM / Ollama 的 /v1 等）：
// baseURL 到 /v1 为止，model 是厂商的模型名。
func WithOpenAI(baseURL, model string, key KeyRef) Option {
	return withEndpoint("openai-chat", baseURL, model, key)
}

// WithOpenAIResponses 同 WithOpenAI，走 OpenAI 的 /responses 接口（推理模型的推理回放）。
func WithOpenAIResponses(baseURL, model string, key KeyRef) Option {
	return withEndpoint("openai-responses", baseURL, model, key)
}

// WithAnthropic 用 Anthropic 的 Messages 接口；model 如 claude-sonnet-4-5。
func WithAnthropic(model string, key KeyRef) Option {
	return withEndpoint("anthropic-messages", "https://api.anthropic.com/v1", model, key)
}

func withEndpoint(protocol, baseURL, model string, key KeyRef) Option {
	return func(c *config) error {
		if strings.TrimSpace(baseURL) == "" || strings.TrimSpace(model) == "" {
			return fmt.Errorf("%w: 模型端点的地址和模型名都要给", ErrInvalid)
		}
		cfg := map[string]any{"base_url": baseURL, "model": model, "protocol": protocol}
		if key != "" {
			cfg["api_key"] = string(key)
		}
		return WithConfig("models", cfg)(c)
	}
}

// WithModel 用内置模型目录里的一个模型或别名（如 deepseek-chat、anthropic/claude-sonnet-4-5），
// 密钥按目录里写的环境变量读（如 DEEPSEEK_API_KEY）。
func WithModel(name string) Option {
	return func(c *config) error {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("%w: WithModel 的名字为空", ErrInvalid)
		}
		return WithConfig("models", map[string]any{"model": name})(c)
	}
}

// WithModelCatalog 用自己的模型目录文件（models.toml：提供方、模型、别名、降级计划），叠在内置目录上。
func WithModelCatalog(path string) Option {
	return func(c *config) error {
		if strings.TrimSpace(path) == "" {
			return fmt.Errorf("%w: WithModelCatalog 的路径为空", ErrInvalid)
		}
		return WithConfig("models", map[string]any{"catalog": path})(c)
	}
}

// WithSecrets 解析密钥引用（接自己的密钥库）：模型目录与 WithOpenAI 等给的引用都交给 f。
// 不给时只认 env:NAME。
func WithSecrets(f func(ref string) (string, error)) Option {
	return func(c *config) error {
		if f == nil {
			return fmt.Errorf("%w: WithSecrets 传了 nil", ErrInvalid)
		}
		c.secrets = f
		return nil
	}
}

type secretsFunc func(string) (string, error)

func (f secretsFunc) Resolve(ref string) (string, error) { return f(ref) }

var _ assemble.Secrets = secretsFunc(nil)
