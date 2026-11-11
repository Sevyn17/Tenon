package component

// 会话作用域（04 4.3.1）：中间件需要「每会话一份」的状态与会话历史回读时用它。
//
// 为什么需要：中间件是全局单例（04 4.4），闭包里不能存会话状态。原来「会话层」靠
// LayerEntry.Func(sc *SessionCx) 每次构造时拿一份，迁到 ToolCall 点位后没有这个入口，
// 所以由 assemble 把一个作用域对象塞进 ToolCallReq。
//
// 语义与 assemble.SessionCx 对齐（但只暴露中间件需要的部分，避免 component 依赖 assemble）。

import (
	"github.com/Sevyn17/tenon/runtime/session"
)

// SessionScope 一个会话的作用域（中间件用）。
//
// 每个会话一个实例，随会话存活。中间件按需在它上面取/存「本会话的状态」，
// 不要存到自己的闭包里（那是跨会话共享的）。
type SessionScope interface {
	// ID 会话 id。
	ID() string
	// Value 取本会话作用域上的一个值（没有返回 nil, false）。
	Value(key string) (any, bool)
	// SetValue 写本会话作用域上的一个值。
	SetValue(key string, v any)
	// History 本会话已提交历史：压缩点与可见窗口里的条目（空 = 不支持回读）。
	History() (compactionBase int, window []*session.Entry)
}
