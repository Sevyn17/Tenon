package component

// SessionOpen 中间件点位（框架设计 04 4.3）：包住「新建一个会话」（02 2.2）。
//
// 典型用途：写入缺省 meta、登记成员、拒绝创建。只在**新建**时跑（打开已有会话不跑）。
// 中间件可改写 Meta（写入缺省业务键）或 Denied（拒绝创建，会话不落库）。

import (
	"context"
	"encoding/json"
)

// PointSessionOpenMw 中间件点位名。
const PointSessionOpenMw = "session_open"

// SessionOpenReq 新建会话的请求。
type SessionOpenReq struct {
	// SessionID 即将建立的会话 id。
	SessionID string
	// Agent 会话绑定的定义名（空 = 无定义会话）。
	Agent string
	// Origin 会话来源（new / fork / side / subagent / member）。
	Origin string
	// Meta 会话业务 meta（JSON 对象）；中间件可改写以写入缺省键。
	Meta json.RawMessage
}

// SessionOpenResp 新建会话的结果。
type SessionOpenResp struct {
	// Denied 拒绝创建：会话不落库，调用方收到错误。
	Denied bool
	// Reason 拒绝原因（进错误信息）。
	Reason string
}

// SessionOpen 中间件点位。
var SessionOpen = NewMwPoint[SessionOpenReq, SessionOpenResp](PointSessionOpenMw)

// SessionOpenMw 便于组件书写的类型别名。
type SessionOpenMw = Middleware[SessionOpenReq, SessionOpenResp]

// SessionOpenChain 把点位上登记的中间件与内核实现串成链。
// kernel 是最内层：真正建会话的那段（Host 提供）。
func (a *Assembly) SessionOpenChain(kernel func(context.Context, SessionOpenReq) (SessionOpenResp, error)) func(context.Context, SessionOpenReq) (SessionOpenResp, error) {
	return Chain(a.middleware[PointSessionOpenMw], kernel)
}
