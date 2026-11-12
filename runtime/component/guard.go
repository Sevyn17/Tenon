package component

import "github.com/Sevyn17/tenon/runtime/mail"

// 判定结论与点位（04 4.2 / 4.3.2）。
//
// 2026-11-12 复核：原 `GuardRule` / `GuardRequest` / `EvalGuards` / `Registrar.Guard` 整套删除——
// 核查发现 `cx.Guard()` 在生产代码里**零登记**（只有 examples 与测试），所以
// `GuardRules()` 恒空 → `Host.Guards` 恒空 → `evalGuard` 恒 Abstain → `toolGuard` 恒 false。
// 整套机制空转，且与 `WrapGuard` 判定区做同一件事。
//
// 现在六个点位的判定统一走 `WrapGuard` + `EvalPoint`（见 guardpoint.go）。
// 本文件只留两个共用类型：`Verdict`（结论）与 `GuardPoint`（点位标识）。

// Verdict 一条判定的结论。
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

// GuardPoint 判定点位（与 04 4.3 的中间件点位同名）。
//
// 2026-11-12：删掉 `GuardTurn` / `GuardModelCall`（零调用点）。
// 剩下的 `GuardDeliver` / `GuardSessionOpen` 由 Host 用来跑该点位的判定区。
type GuardPoint int

const (
	// GuardDeliver 信封投递前。
	GuardDeliver GuardPoint = iota
	// GuardSessionOpen 新建会话前。
	GuardSessionOpen
)

func (p GuardPoint) String() string {
	switch p {
	case GuardDeliver:
		return "deliver"
	case GuardSessionOpen:
		return "session_open"
	default:
		return "unknown"
	}
}

// GuardRequest 一次判定携带的信息（哪些字段有值由点位决定）。
//
// 2026-11-12：原 `GuardRule` / `EvalGuards` / `Registrar.Guard` 删除（生产零登记，整套空转）；
// 但 `GuardRequest` 保留——它是 `Host` 跑投递/建会话判定区时传给 `EvalPoint` 的请求体，
// 与 `WrapGuard` 的 `GuardFn[Req]` 是同一套。
type GuardRequest struct {
	Point    GuardPoint
	Envelope *mail.Envelope // GuardDeliver、GuardSessionOpen 时有值
	Agent    string         // 目标定义名，GuardSessionOpen 时有值
}
