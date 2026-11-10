package tenon

// 写组件用的声明、服务、守卫、点位与登记项（计划/08 3.5、3.6 上手 03）：组件只 import 本包就能声明配置、
// 提供与使用服务、登守卫规则，以及在 Setup 里用 RegisterAt(cx, tenon.PointTools, 名字, 登记项) 登记。
// 登记项的类型由点位决定，写错编译不过。常用点位和登记项类型都在这里，写组件不必 import runtime/*；
// 完整的点位列表见 runtime/assemble。

import (
	"strings"

	"github.com/Sevyn17/tenon/runtime/assemble"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/ext"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// ComponentInfo 组件的可选声明。组件再实现 Info() ComponentInfo 时按它装配；不实现等于全空。
type ComponentInfo struct {
	// Config 配置结构体的零值指针：字段用 cfg tag 取键名、default tag 写缺省值。
	// WithConfig(组件名, …) 按它校验（未知键在 New 里报错），Setup 里用 cx.Config() 取解码后的同类型指针。
	Config any
	// Provides 本组件提供的服务键（Key.Name()）；Setup 里用 Provide 登记。
	Provides []string
	// Requires 本组件要用的服务键：必需写 Key.Need()，可选写 Key.Maybe()；Setup 里用 Get / GetOptional 取。
	Requires []string
}

// Key 带类型的服务键：服务名和它的 Go 类型绑在一起。用 NewKey 构造。
type Key[T any] = ext.Key[T]

// NewKey 构造一个服务键；name 是服务名（不带 service: 前缀）。
func NewKey[T any](name string) Key[T] { return ext.NewKey[T]("service:" + name) }

// Provide 在 Setup 里登记本组件提供的服务；k 必须写在 ComponentInfo.Provides 里。
func Provide[T any](cx *Registrar, k Key[T], v T) error {
	return cx.Provide(strings.TrimPrefix(k.Name(), "service:"), v)
}

// Get 在 Setup 里取一项必需服务；k 必须写在 ComponentInfo.Requires 里（k.Need()）。
func Get[T any](cx *Registrar, k Key[T]) (T, error) { return component.Get(cx, k) }

// GetOptional 取一项可选服务（Requires 里写 k.Maybe()）；提供者不在装配里时 ok 为 false。
func GetOptional[T any](cx *Registrar, k Key[T]) (T, bool, error) {
	return component.GetOptional(cx, k)
}

// 守卫：组件在 Setup 里用 cx.Guard(GuardRule{…}) 登记。Check 返回 (结论, 拒绝原因)。
// 同一位置上任何一条 Deny 就拒绝，其余按内核缺省处理；Allow 只放开"往 session: 投递"的缺省限制。
// 投递与建会话被拒时原因进 guard.denied 事件；工具调用被拒时原因作为失败结果交给模型，这一轮继续。
type (
	GuardRule    = component.GuardRule
	GuardRequest = component.GuardRequest
	GuardPoint   = component.GuardPoint
	Verdict      = component.Verdict
)

// 守卫位置。内核目前只在这三处执行规则。
const (
	GuardDeliver     = component.GuardDeliver     // 信封投递（GuardRequest.Envelope）
	GuardSessionOpen = component.GuardSessionOpen // 新建会话（GuardRequest.Agent 是定义名）
	GuardToolCall    = component.GuardToolCall    // 调工具（GuardRequest.Tool、Args、Risk）
)

// 守卫结论。
const (
	Abstain = component.Abstain
	Allow   = component.Allow
	Deny    = component.Deny
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
