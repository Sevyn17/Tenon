package component

// 中间件点位的具体定义（框架设计 04 4.3）。
//
// 先落 Deliver 一个点位（T6b 最需要、语义最清晰）；其余点位（SessionOpen / Turn / Context /
// ModelCall / ToolCall）随各自内核路径改造逐步接。已有近似实现（SessionLayer 做 ToolCall、
// BlockFunc 做 Context）在点位落地时收进去，不另写一套。

import (
	"context"

	"github.com/Sevyn17/tenon/runtime/mail"
)

// 中间件点位名（explain 与诊断用）。
const PointDeliverMw = "deliver"

// DeliverReq 一次「把信封投进会话信箱」的请求（04 4.3）。
// 中间件可以改写 Envelope（过滤 / 改投别的 session:）或直接短路。
type DeliverReq struct {
	// Envelope 待投递的信封。中间件可改 To（转投别的 session:）、Mode、Body 等。
	Envelope *mail.Envelope
	// SessionID 目标会话 id（Envelope.To 解析后的）。
	SessionID string
	// FromRemote 本次投递是否来自远端（跨机器）。
	FromRemote bool
}

// DeliverResp 投递结果。
type DeliverResp struct {
	// Delivered 是否真的投进了信箱；false 表示被中间件拦下（不报错）。
	Delivered bool
}

// Deliver 中间件点位（04 4.3）：包住「一封信封投递到会话信箱」。
// 典型用途：过滤、改写、按规则转投到别的 session:。
var Deliver = NewMwPoint[DeliverReq, DeliverResp](PointDeliverMw)

// DeliverMw 便于组件书写的类型别名。
type DeliverMw = Middleware[DeliverReq, DeliverResp]

// DeliverChain 把点位上登记的中间件与内核实现串成链。
// kernel 是最内层：真正把信封投进信箱的那段（Host 提供）。
func (a *Assembly) DeliverChain(kernel func(context.Context, DeliverReq) (DeliverResp, error)) func(context.Context, DeliverReq) (DeliverResp, error) {
	return Chain(a.middleware[PointDeliverMw], kernel)
}

// HasMiddleware 某点位上是否登记了中间件。
func (a *Assembly) HasMiddleware(point string) bool { return len(a.middleware[point]) > 0 }
