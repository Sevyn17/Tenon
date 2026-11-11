// Package extproc 扩展进程协议（框架设计 04 4.8，T6b）：stdio 上的 JSON-RPC 2.0，MCP 的超集。
//
// 本包只做协议层：消息编解码、握手（报告要登记的点位）、点位范围校验。进程监管（起进程、
// 退避重启、杀进程树）与回桥（把工具调用/中间件/事件投给进程）在组件层用 modules/internal/proc 接。
//
// 硬边界（04 4.8）：只开这些能力——工具、ToolCall 与 Deliver 两个中间件点位、事件订阅、匹配器、
// 资源类型、渲染器。明确不开：Guard、Provide 服务、ModelCall 与 Context 点位。守卫规则只能来自
// 编译进程序的代码，所以握手报告越界点位时必须被拒。
package extproc

import (
	"encoding/json"
	"fmt"
)

// 协议版本。握手双方须一致。
const Version = "1"

// 允许的能力（04 4.8 硬边界）。握手报告的每一项必须在这个集合里。
const (
	CapTools      = "tools"      // 工具
	CapMiddleware = "middleware" // ToolCall / Deliver 两个中间件点位
	CapEvents     = "events"     // 事件订阅
	CapMatcher    = "matcher"    // 订阅匹配器
	CapResource   = "resource"   // 资源类型
	CapRenderer   = "renderer"   // 渲染器
)

// AllowedCaps 允许的能力集合。
var AllowedCaps = map[string]bool{
	CapTools: true, CapMiddleware: true, CapEvents: true,
	CapMatcher: true, CapResource: true, CapRenderer: true,
}

// MiddlewarePoints 中间件只开这两个点位。
var MiddlewarePoints = map[string]bool{"tool_call": true, "deliver": true}

// Request JSON-RPC 2.0 请求。
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response JSON-RPC 2.0 响应。
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError JSON-RPC 错误。
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("extproc: %d %s", e.Code, e.Message) }

// Handshake 握手：插件进程报告要登记的能力与点位。
type Handshake struct {
	Version string `json:"version"`
	// Capabilities 报告的能力，取自 Cap* 常量。
	Capabilities []string `json:"capabilities"`
	// Middleware 中间件点位名（只允许 tool_call / deliver）。
	Middleware []string `json:"middleware,omitempty"`
	// Tools 工具名清单。
	Tools []string `json:"tools,omitempty"`
	// Matchers 匹配器名清单。
	Matchers []string `json:"matchers,omitempty"`
	// Resources 资源类型名清单。
	Resources []string `json:"resources,omitempty"`
}

// Validate 校验握手：版本一致、能力在允许集合内、中间件点位合法。
// 越界（含 Guard / Provide / ModelCall / Context）一律拒绝——这是硬边界。
func (h Handshake) Validate() error {
	if h.Version != Version {
		return fmt.Errorf("extproc: 协议版本 %q 与本端 %q 不一致", h.Version, Version)
	}
	if len(h.Capabilities) == 0 {
		return fmt.Errorf("extproc: 握手未报告任何能力")
	}
	for _, c := range h.Capabilities {
		if !AllowedCaps[c] {
			return fmt.Errorf("extproc: 能力 %q 不在允许范围（只开 tools/middleware/events/matcher/resource/renderer；守卫、服务、ModelCall、Context 一律不开）", c)
		}
	}
	for _, m := range h.Middleware {
		if !MiddlewarePoints[m] {
			return fmt.Errorf("extproc: 中间件点位 %q 不在允许范围（只开 tool_call / deliver）", m)
		}
	}
	return nil
}

// Encode 编码一个请求。
func Encode(r Request) ([]byte, error) {
	if r.JSONRPC == "" {
		r.JSONRPC = "2.0"
	}
	return json.Marshal(r)
}

// DecodeRequest 解码一个请求（校验 jsonrpc 版本）。
func DecodeRequest(b []byte) (Request, error) {
	var r Request
	if err := json.Unmarshal(b, &r); err != nil {
		return r, err
	}
	if r.JSONRPC != "2.0" {
		return r, fmt.Errorf("extproc: 不是 JSON-RPC 2.0（jsonrpc=%q）", r.JSONRPC)
	}
	if r.Method == "" {
		return r, fmt.Errorf("extproc: 请求缺 method")
	}
	return r, nil
}

// EncodeResponse 编码一个响应。
func EncodeResponse(r Response) ([]byte, error) {
	if r.JSONRPC == "" {
		r.JSONRPC = "2.0"
	}
	return json.Marshal(r)
}
