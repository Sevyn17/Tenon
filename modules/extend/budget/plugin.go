// Package budget 登记 [use.budget] 的使用配置 schema，并按本轮触发者
// 解析预算层级（框架设计 08 8.4、06 6.3）。
package budget

// 预算使用配置（框架设计 08 8.4、06 6.3，T4f/T5c 基础设施）：登记 [use.budget] 的 schema，
// 让定义可以声明每日 token 上限。记账与挂起在 runtime/host（budget.go）；本组件只负责
// 让 [use.budget] 通过使用配置链的校验与三层取最小（limit:"min"）。

import (
	"github.com/Sevyn17/tenon/runtime/component"
)

// ServiceName 组件名（[use.budget]）。
const ServiceName = "budget"

// UseConfig 定义里的 [use.budget]。
//
//	[use.budget]
//	tokens_per_day = 200000
type UseConfig struct {
	// TokensPerDay 每日 token 上限（08 8.4）。limit:"min"：定义 / 租户 / 用户各层取最小。
	TokensPerDay int64 `toml:"tokens_per_day" tenant:"true" limit:"min" description:"每日 token 上限；租户/用户可覆盖，各层取最小"`
}

type plug struct{}

func init() { component.Register(ServiceName, func() component.Component { return &plug{} }) }

func (*plug) Meta() component.Meta {
	return component.Meta{Name: ServiceName, Version: "0.1.0", Default: true}
}

func (*plug) Setup(cx *component.Registrar) error {
	return cx.AgentConfig(&UseConfig{})
}

func (*plug) Start(*component.Scope) error { return nil }
