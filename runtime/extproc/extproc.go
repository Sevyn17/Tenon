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
	// ToolSpecs 工具的完整声明（名字、描述、JSON Schema）；有它就按它登记工具。
	ToolSpecs []ToolSpec `json:"tool_specs,omitempty"`
	// Matchers 匹配器名清单。
	Matchers []string `json:"matchers,omitempty"`
	// Events 订阅的事件名清单（名字可 * 结尾表示前缀）。
	Events []string `json:"events,omitempty"`
	// Resources 资源类型名清单。
	Resources []string `json:"resources,omitempty"`
}

// ToolSpec 一个外部工具的声明（模型面）。
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema,omitempty"`
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
	// 报告了事件订阅就必须声明 events 能力。
	if len(h.Events) > 0 && !hasCap(h.Capabilities, CapEvents) {
		return fmt.Errorf("extproc: 报告了事件订阅但没声明 %q 能力", CapEvents)
	}
	// 报告了中间件就必须声明 middleware 能力。
	if len(h.Middleware) > 0 && !hasCap(h.Capabilities, CapMiddleware) {
		return fmt.Errorf("extproc: 报告了中间件但没声明 %q 能力", CapMiddleware)
	}
	// 报告了匹配器 / 资源类型就必须声明对应能力。
	if len(h.Matchers) > 0 && !hasCap(h.Capabilities, CapMatcher) {
		return fmt.Errorf("extproc: 报告了匹配器但没声明 %q 能力", CapMatcher)
	}
	if len(h.Resources) > 0 && !hasCap(h.Capabilities, CapResource) {
		return fmt.Errorf("extproc: 报告了资源类型但没声明 %q 能力", CapResource)
	}
	// 报告了工具就必须声明 tools 能力，避免绕过能力声明登记点位。
	if len(h.ToolSpecs) > 0 && !hasCap(h.Capabilities, CapTools) {
		return fmt.Errorf("extproc: 报告了工具但没声明 %q 能力", CapTools)
	}
	for _, t := range h.ToolSpecs {
		if t.Name == "" {
			return fmt.Errorf("extproc: 工具缺名字")
		}
	}
	return nil
}

// hasCap 能力集合里是否有 c。
func hasCap(caps []string, c string) bool {
	for _, x := range caps {
		if x == c {
			return true
		}
	}
	return false
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

// MiddlewareCall 中间件转发参数（框架 → 插件）。
type MiddlewareCall struct {
	// Point 中间件点位：tool_call / deliver。
	Point string `json:"point"`
	// Tool 工具名（point=tool_call 时）。
	Tool string `json:"tool,omitempty"`
	// Args 工具参数（point=tool_call 时，JSON 原文）。
	Args json.RawMessage `json:"args,omitempty"`
	// SessionID 会话 id。
	SessionID string `json:"session_id,omitempty"`
	// Envelope 信封（point=deliver 时）。
	Envelope json.RawMessage `json:"envelope,omitempty"`
}

// MiddlewareResult 中间件结果（插件 → 框架）：Verdict 空 = 放行；deny = 拒绝（Reason 给原因）。
type MiddlewareResult struct {
	Verdict string `json:"verdict,omitempty"`
	Reason  string `json:"reason,omitempty"`
	// Args 改写后的参数（可空；transform 类中间件用）。
	Args json.RawMessage `json:"args,omitempty"`
}

// EventNotice 事件通知参数（框架 → 插件，无 id 的通知）。
type EventNotice struct {
	Name string          `json:"name"`
	At   int64           `json:"at,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}
