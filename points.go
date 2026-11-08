package tenon

// 写组件用的点位与登记项（计划/08 3.5）：组件在 Setup 里用 RegisterAt(cx, tenon.PointTools, 名字, 登记项) 登记，
// 登记项的类型由点位决定，写错编译不过。常用点位和登记项类型都在这里，写组件不必 import runtime/*；
// 完整的点位列表见 runtime/assemble。

import (
	"tenon/runtime/assemble"
	"tenon/runtime/component"
	"tenon/runtime/ext"
	"tenon/runtime/session"
	"tenon/runtime/toolhub"
)

// Point 类型化点位：T 是这个点位上登记项的类型。
type Point[T any] = ext.Point[T]

type (
	// ToolsEntry 一批会话工具（PointTools）。Visible 可空：按会话决定要不要这批工具。
	ToolsEntry = assemble.ToolsEntry
	// BlockEntry 一个系统提示块（PointSystemBlock）。
	BlockEntry = assemble.BlockEntry
	// LayerEntry 一个会话层（PointLayer）：Guard 为 true 是守卫（拦截调用），否则是变换。
	LayerEntry = assemble.LayerEntry
	// AfterTurnFunc 回合结束通知（PointAfterTurn）。
	AfterTurnFunc = assemble.AfterTurnFunc
	// SessionCx 构造会话时登记项拿到的上下文（会话 id、定义、工作目录……）。
	SessionCx = assemble.SessionCx
	// SystemBlock 一段系统提示（BlockEntry.Func 返回它）：Text 是正文，Breakpoint 为 true 时在块后打缓存断点。
	SystemBlock = session.SystemBlock
	// EndReason 回合结束原因（AfterTurnFunc 的第二个参数）：Code / Message 在失败时有值。
	EndReason = session.EndReason
)

var (
	// PointTools 会话工具：登记 ToolsEntry。
	PointTools = assemble.SessionTools
	// PointSystemBlock 系统提示块：登记 BlockEntry。
	PointSystemBlock = assemble.SystemBlock
	// PointLayer 会话层（守卫与变换）：登记 LayerEntry。
	PointLayer = assemble.SessionLayer
	// PointAfterTurn 回合结束通知：登记 AfterTurnFunc。
	PointAfterTurn = assemble.AfterTurn
)

// RegisterAt 在类型化点位上登记一项；impl 的类型由点位决定。
func RegisterAt[T any](cx *Registrar, p Point[T], name string, impl T) {
	component.RegisterAt(cx, p, name, impl)
}

// ToolsOf 把 Func / Raw 做的工具包成 PointTools 的登记项；order 决定工具在请求里的位置（内置工具在 10–90）。
func ToolsOf(order int, ts ...Tool) ToolsEntry {
	list := make([]*toolhub.Tool, 0, len(ts))
	for _, t := range ts {
		if t.t != nil {
			list = append(list, t.t)
		}
	}
	return ToolsEntry{Order: order, Tools: func() []*toolhub.Tool { return list }}
}
