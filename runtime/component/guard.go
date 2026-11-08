package component

import (
	"encoding/json"

	"tenon/runtime/mail"
)

// 守卫规则（04 4.2、08 8.1 / D4）。组件在 Setup 里通过 Registrar.Guard 登记，
// 守卫在内核里、紧贴投递和调用执行，中间件和模型绕不过去。
//
// 合并规则：任一条 Deny 即拒绝；全部 Abstain 走内核缺省；Allow 只放开
// 「往 session: 投递」的缺省限制，放不开联系名单、预算、跳数和租户边界。

// Verdict 一条规则的结论。
type Verdict int

const (
	// Abstain 不表态，交给其他规则和内核缺省。
	Abstain Verdict = iota
	// Allow 允许（只对往 session: 的投递生效）。
	Allow
	// Deny 拒绝。
	Deny
)

func (v Verdict) String() string {
	switch v {
	case Allow:
		return "allow"
	case Deny:
		return "deny"
	default:
		return "abstain"
	}
}

// GuardPoint 守卫点位（与 04 4.3 的中间件点位同名）。
type GuardPoint int

const (
	// GuardDeliver 信封投递前。
	GuardDeliver GuardPoint = iota
	// GuardSessionOpen 新建会话前。
	GuardSessionOpen
	// GuardTurn 一轮开始或恢复前。
	GuardTurn
	// GuardModelCall 调模型前。
	GuardModelCall
	// GuardToolCall 调工具前。
	GuardToolCall
)

func (p GuardPoint) String() string {
	switch p {
	case GuardDeliver:
		return "deliver"
	case GuardSessionOpen:
		return "session_open"
	case GuardTurn:
		return "turn"
	case GuardModelCall:
		return "model_call"
	case GuardToolCall:
		return "tool_call"
	default:
		return "unknown"
	}
}

// Risk 工具风险（04 4.2）。
type Risk int

const (
	RiskLow Risk = iota
	// RiskHigh external 触发时要人工确认（08 8.5）。
	RiskHigh
)

// GuardRequest 一次守卫检查携带的信息（哪些字段有值由 Point 决定）。
type GuardRequest struct {
	Point    GuardPoint
	Envelope *mail.Envelope // GuardDeliver、GuardSessionOpen 时有值
	Agent    string         // 目标定义名，GuardSessionOpen 时有值
	Tool     string         // GuardToolCall 时有值
	Args     json.RawMessage
	Risk     Risk   // GuardToolCall 时为工具声明的风险
	Model    string // GuardModelCall 时有值
}

// GuardRule 一条守卫规则。Check 返回 (结论, 拒绝原因)；原因进 guard.denied 事件。
type GuardRule struct {
	Name  string
	On    GuardPoint
	Check func(req GuardRequest) (Verdict, string)
}

// Guard 向守卫登记规则。规则只能来自编译进程序的组件（04 4.8）。
func (cx *Registrar) Guard(g GuardRule) {
	name := g.Name
	if name == "" {
		name = cx.plugin
	}
	cx.asm.guards = append(cx.asm.guards, GuardRule{Name: name, On: g.On, Check: g.Check})
	cx.asm.points = append(cx.asm.points, Registration{Plugin: cx.plugin, Point: "guard", Name: name})
}

// GuardRules 已登记的守卫规则（按装配顺序）。
func (a *Assembly) GuardRules() []GuardRule {
	out := make([]GuardRule, len(a.guards))
	copy(out, a.guards)
	return out
}

// EvalGuards 跑完 req.Point 上的全部规则并合并结论（04 4.2、08 D4）：任一条 Deny 即拒绝（原因为空时用规则名）；
// 有 Allow 结论为 Allow；全部 Abstain 为 Abstain，由调用方走内核缺省。
func EvalGuards(rules []GuardRule, req GuardRequest) (Verdict, string) {
	verdict, reason := Abstain, ""
	for _, r := range rules {
		if r.On != req.Point || r.Check == nil {
			continue
		}
		v, why := r.Check(req)
		switch v {
		case Deny:
			if why == "" {
				why = r.Name
			}
			return Deny, why
		case Allow:
			verdict = Allow
			if why != "" {
				reason = why
			}
		}
	}
	return verdict, reason
}
