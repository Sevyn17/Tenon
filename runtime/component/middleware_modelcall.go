package component

// ModelCall 中间件点位（框架设计 04 4.3）：包住「一次模型请求」。
//
// 典型用途：脱敏、换模型、记账、重试。
//
// **换模型的代价**（4.3、12 12.3）：换模型会让前缀缓存失效，应只在必要时用。
// 中间件改写 Req.Request.Model 只影响本次请求；内核记录的 model 仍以响应里的为准。

import (
	"context"

	"github.com/Sevyn17/tenon/runtime/session"
)

// PointModelCallMw 中间件点位名。
const PointModelCallMw = "model_call"

// ModelCallReq 一次模型请求。
type ModelCallReq struct {
	// SessionID 会话 id。
	SessionID string
	// RequestID 本次请求 id（R3，日志与重试用）。
	RequestID string
	// Turn / Step 回合号与步号。
	Turn uint32
	Step uint32
	// Request 发给模型的请求。中间件可脱敏（改 Messages）、换模型（改 Model）。
	// **注意**：换模型会让前缀缓存失效（12 12.3），只在必要时用。
	Request *session.Request
	// Retried 本次是否是「上下文超限后强制压缩重发」的那一次。
	Retried bool
}

// ModelCallResp 模型请求的结果。
type ModelCallResp struct {
	// Reply 模型回复。中间件可改写（如补全 usage、清洗内容）。
	Reply session.AssistantItem
	// Stop 停止原因。
	Stop session.StopReason
}

// ModelCall 中间件点位。
var ModelCall = NewMwPoint[ModelCallReq, ModelCallResp](PointModelCallMw)

// ModelCallMw 便于组件书写的类型别名。
type ModelCallMw = Middleware[ModelCallReq, ModelCallResp]

// ModelCallChain 把点位上登记的中间件与内核实现串成链。
func (a *Assembly) ModelCallChain(kernel func(context.Context, ModelCallReq) (ModelCallResp, error)) func(context.Context, ModelCallReq) (ModelCallResp, error) {
	return Chain(a.middleware[PointModelCallMw], kernel)
}
