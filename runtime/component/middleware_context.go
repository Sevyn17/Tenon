package component

// Context 中间件点位（框架设计 04 4.3）：包住「组装发给模型的上下文」。
//
// **三条硬限制**（4.3，B15/B16）：
//  1. 不能改系统提示词 —— 系统提示词只来自定义和框架（12 12.5 的 L0–L1）。
//     中间件拿到的 System 只读；内核在链之后会把它还原成内核版本。
//  2. 不能加不进历史的注入 —— 要让模型看到会变的信息，只能走 Registrar.Status（12 12.5）。
//  3. 能做的是：重排、改写历史条目的渲染格式，以及在组装时附加进历史的内容。
//
// 典型用途：记忆召回、多人发言的格式。

import (
	"context"
	"encoding/json"

	"github.com/Sevyn17/tenon/runtime/session"
)

// PointContextMw 中间件点位名。
const PointContextMw = "context"

// ContextReq 一次上下文组装的请求。
type ContextReq struct {
	// SessionID 会话 id。
	SessionID string
	// Turn 回合号。
	Turn uint32
	// Request 组装好的请求。中间件可改 Messages（重排 / 改写渲染 / 附加进历史的内容）；
	// **改 System 无效**：内核在链之后会把它还原（4.3 限制 1）。
	Request *session.Request
	// System 内核版系统提示词的只读拷贝（中间件可读，改了不生效）。
	System []session.SystemBlock
}

// ContextResp 上下文组装的结果。
type ContextResp struct {
	// Messages 改写后的历史。为空表示中间件没改（用 Request.Messages）。
	Messages []session.Message
}

// Context 中间件点位。
var Context = NewMwPoint[ContextReq, ContextResp](PointContextMw)

// ContextMw 便于组件书写的类型别名。
type ContextMw = Middleware[ContextReq, ContextResp]

// ContextChain 把点位上登记的中间件与内核实现串成链。
func (a *Assembly) ContextChain(kernel func(context.Context, ContextReq) (ContextResp, error)) func(context.Context, ContextReq) (ContextResp, error) {
	return Chain(a.middleware[PointContextMw], kernel)
}

// 说明：ContextReq.Data 之类的跨中间件传递不做——4.3 限制 2 要求「不进历史的信息只能走
// Registrar.Status」，中间件之间也不该借 Context 传状态。需要跨中间件协作就写进会话或集合。

var _ = json.Marshal
