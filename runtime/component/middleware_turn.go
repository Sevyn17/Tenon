package component

// Turn 中间件点位（框架设计 04 4.3）：包住「一轮运行中的一段」（02 2.4）——
// 从取信或恢复开始，到这一轮完成、挂起或中断为止。
//
// 典型用途：回合前准备、回合后处理、压缩。
//
// **不跨挂起**（4.3 硬约束）：一轮等工具、等人、预算挂起或进程重启后，Turn 中间件链在
// 恢复时**重新调用一次**，`Resumed = true`。中间件不能把状态放在闭包或调用栈里等它「回来」，
// 需要跨段保留的状态写进会话或组件集合。

import (
	"context"
	"encoding/json"
)

// PointTurnMw 中间件点位名。
const PointTurnMw = "turn"

// TurnReq 一轮运行的请求。
type TurnReq struct {
	// SessionID 会话 id。
	SessionID string
	// Turn 回合号。
	Turn uint32
	// Resumed 这一轮是否是崩溃恢复后重跑的（4.3：恢复时链重跑一次，Resumed=true）。
	Resumed bool
	// Inputs 本轮吃进的输入（用户消息 / 信封）的 client_id 列表。
	Inputs []string
	// Data 中间件之间传递的会话级数据（不进模型、不落库）；用于同一轮内多次链调用的配合。
	Data map[string]json.RawMessage
}

// TurnResp 一轮运行的结果。
//
// 没有「跳过内核」这一项：Turn 中间件是包在内核外面的一层（4.3「洋葱式」），
// 跳过内核会让调用方 Wait 的语义断裂（回合没跑，但调用方在等它结束）。
// 要拦截整轮，应由守卫（GuardTurn）或权限层做，不是中间件。
type TurnResp struct {
	// End 回合结束原因（内核填）。中间件可改写（如把挂起改成结束）。
	End json.RawMessage
}

// Turn 中间件点位。
var Turn = NewMwPoint[TurnReq, TurnResp](PointTurnMw)

// TurnMw 便于组件书写的类型别名。
type TurnMw = Middleware[TurnReq, TurnResp]

// TurnChain 把点位上登记的中间件与内核实现串成链。
func (a *Assembly) TurnChain(kernel func(context.Context, TurnReq) (TurnResp, error)) func(context.Context, TurnReq) (TurnResp, error) {
	return Chain(a.middleware[PointTurnMw], kernel)
}
