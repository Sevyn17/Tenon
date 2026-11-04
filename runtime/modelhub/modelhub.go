// Package modelhub：模型管理器（model-hub 定稿 D1–D7，review-1 R2 / R3 / G5）。
//
// 两层：协议适配器（代码，少）+ 提供方（声明式数据，多）。别名展开为降级计划，
// 调度器逐项单次调用适配器；重试、熔断、超时只在调度器内，不进会话日志（D1）。
package modelhub

import (
	"context"
	"fmt"
	"time"

	"ybase/runtime/session"
)

// Code 稳定失败码：上层只按 code 分支，不解析消息文本。
type Code string

const (
	CodeNoAdapter       Code = "NO_ADAPTER"
	CodeUnknownModel    Code = "UNKNOWN_MODEL" // 目录里没有这个模型或别名（原先归为 NO_ADAPTER）
	CodeMissingCred     Code = "MISSING_CREDENTIAL"
	CodeAuth            Code = "AUTH"
	CodeInvalidRequest  Code = "INVALID_REQUEST"
	CodeRateLimit       Code = "RATE_LIMIT"
	CodeQuota           Code = "QUOTA"
	CodeServer          Code = "SERVER"
	CodeTimeout         Code = "TIMEOUT"
	CodeTransport       Code = "TRANSPORT"
	CodeEmptyResponse   Code = "EMPTY_RESPONSE"
	CodeContextOverflow Code = "CONTEXT_WINDOW_EXCEEDED"
	CodeAborted         Code = "ABORTED"
	CodeCircuitOpen     Code = "CIRCUIT_OPEN"
	CodeProtocol        Code = "PROTOCOL"
	CodeShape           Code = "REQUEST_SHAPE" // R2：Transform 违反结构约束
)

// Retryable 可在同一计划项内重试的 code。
func (c Code) Retryable() bool {
	switch c {
	case CodeRateLimit, CodeServer, CodeTimeout, CodeTransport, CodeEmptyResponse:
		return true
	}
	return false
}

// Failure 终止失败。
type Failure struct {
	Code       Code
	Message    string
	RetryAfter time.Duration
	Partial    *session.AssistantItem // 已收到的部分（中断时写 interrupted 条目）
}

func (f *Failure) Error() string { return string(f.Code) + ": " + f.Message }

// Event 流事件。恰好一个终止事件（Done 或 Failed），之后通道关闭。
// 全部字段为空的事件是心跳：只重置调度器的首事件 / 空闲计时，不改变任何状态。适配器在收到
// 不对外展示的增量（工具参数、推理签名）时发它——否则模型一开口就流一个大工具参数时，
// 一分钟以上没有事件，被判首事件超时再重试（2026-11-04 审查 P0-13）。
type Event struct {
	TextDelta      string
	ReasoningDelta string
	Reset          bool // StreamReset：之前的增量作废（重试 / 换项前发出）
	Done           *Done
	Failed         *Failure
}

// Done 成功终止。
type Done struct {
	Reply session.AssistantItem
	Stop  session.StopReason
}

// Endpoint 一次调用的目标：提供方解析后的连接信息。
type Endpoint struct {
	Provider string
	BaseURL  string
	APIKey   string
	Model    string // 厂商模型名
	Compat   Compat
}

// Compat 兼容标志（数据，不为每个厂商分叉适配器，D2）。
type Compat struct {
	PromptCacheKey bool   `toml:"prompt_cache_key"` // 支持 prompt_cache_key 请求字段
	ReasoningField string `toml:"reasoning_field"`  // 推理增量字段名（reasoning_content / reasoning）
	MaxTokensField string `toml:"max_tokens_field"`
}

// Request 模型调用请求。
type Request struct {
	Req        session.Request
	RequestID  string // need id：回交内核用（R3）
	AttemptKey string // 发厂商的幂等键 = need id + 计划项序号（R3），调度器填
	SessionKey string // 会话缓存亲和 id（D5）
}

// Adapter 协议适配器：单次流式调用，不重试、不聚合、不计量。
type Adapter interface {
	Protocol() string
	Stream(ctx context.Context, req *Request, ep Endpoint) <-chan Event
}

// Capabilities 能力（D7：未声明继承协议默认；智能体定义与回合设置只能收窄）。
type Capabilities struct {
	Reasoning     *bool `toml:"reasoning"`
	Images        *bool `toml:"images"`
	ParallelTools *bool `toml:"parallel_tools"`
}

// Model 目录条目。
type Model struct {
	Provider      string       `toml:"provider"`
	Name          string       `toml:"name"` // 厂商模型名；空 = 键名
	ContextWindow int          `toml:"context_window"`
	MaxOutput     int          `toml:"max_output"`
	Caps          Capabilities `toml:"caps"`
	source        string
}

// Provider 提供方（声明式）。
type Provider struct {
	Protocol string `toml:"protocol"`
	BaseURL  string `toml:"base_url"`
	Key      string `toml:"key"` // 凭证引用：env:NAME / keychain:NAME
	Compat   Compat `toml:"compat"`
	Retry    Retry  `toml:"retry"`
	source   string
}

// Retry 提供方重试策略（D1：按提供方配置）。
type Retry struct {
	Max int `toml:"max"`
}

// PlanStep 降级计划的一项。
type PlanStep struct {
	Model   string `toml:"model"`
	Retries int    `toml:"retries"` // 0 = 取提供方 / 默认
}

// Plan 别名展开的降级计划。
type Plan []PlanStep

// ErrUnknownAlias 别名不存在。
type ErrUnknownAlias struct{ Alias string }

func (e ErrUnknownAlias) Error() string {
	return fmt.Sprintf("modelhub: 未知模型别名 %q", e.Alias)
}
