package toolhub

// 授权判定（04 4.3.1）：工具管理器不自己做授权，但需要一个「谁来授权」的落点。
//
// 原来靠「恰好一层 Authorizes=true 的 Layer」表达（items.go 的 Hub.Validate 强制唯一）。
// 04 4.3.1 把决策从「拦截链」里拆出来：授权是**一个决策者**（可挂起问人、有状态），
// 不是链上的一层。所以改成 Hub.Authorizer 字段。
//
// 兼容：Authorizes 层仍然工作（迁移期间两者都在）；装配层在迁移完成后只设 Authorizer。

import (
	"context"
	"encoding/json"
)

// Decision 一次授权判定的结论。
type Decision struct {
	// Action 允许 / 拒绝 / 需要确认。
	Action DecisionAction
	// Reason 原因（拒绝与确认时给用户看）。
	Reason string
}

// DecisionAction 判定动作。
type DecisionAction int

const (
	// DecisionAllow 允许执行。
	DecisionAllow DecisionAction = iota
	// DecisionDeny 拒绝。
	DecisionDeny
	// DecisionConfirm 需要人工确认（挂起，等用户批准后按同一参数重放）。
	DecisionConfirm
)

// Authorizer 工具调用的授权判定者（04 4.3.1）。
//
// 与 Layer 的区别：Authorizer 是**一个决策者**（不是链上的一层），可以挂起问人、有状态；
// 返回值里 DecisionConfirm 让工具管理器进入「等确认」流程。
type Authorizer interface {
	// Authorize 判定一次工具调用。args 是最终参数（中间件改写之后）。
	Authorize(ctx context.Context, cx *Cx, args json.RawMessage) (Decision, error)
}

// AuthorizerFunc 把函数适配成 Authorizer。
type AuthorizerFunc func(ctx context.Context, cx *Cx, args json.RawMessage) (Decision, error)

// Authorize 实现 Authorizer。
func (f AuthorizerFunc) Authorize(ctx context.Context, cx *Cx, args json.RawMessage) (Decision, error) {
	return f(ctx, cx, args)
}

// AddAuthorizer 追加一个授权判定者（04 4.3.1）。多个判定者按「取最严」组合。
//
// 为什么是追加而不是覆盖：permission（档位/规则）与 sandbox（沙箱升级申请）是两个
// 独立策略，各自在自己的组件里登记；内核把它们组合，不让组件之间互相知道。
func (h *Hub) AddAuthorizer(a Authorizer) {
	if a == nil {
		return
	}
	h.authorizer = ChainAuthorizers(h.authorizer, a)
}

// SetAuthorizer 设授权判定者（覆盖）。装配层或测试用；组件请用 AddAuthorizer。
func (h *Hub) SetAuthorizer(a Authorizer) { h.authorizer = a }

// HasAuthorizer 是否已设授权判定者。
func (h *Hub) HasAuthorizer() bool { return h.authorizer != nil }

// ChainAuthorizers 把多个决策者组合成一个（04 4.3.1）。
//
// 组合规则与守卫一致：**取最严**——任一 Deny 即拒；否则任一 Confirm 即需确认；
// 全 Allow 才 Allow。原因取第一个非空（按传入顺序，确定性）。
//
// 为什么需要组合：permission（档位/规则）与 sandbox（沙箱升级申请）是两个独立策略，
// 但 Hub 只能有一个 Authorizer。组合让它们各自表达，由内核统一取严。
func ChainAuthorizers(as ...Authorizer) Authorizer {
	// 过滤 nil，避免调用方为「可能没装」写分支。
	var xs []Authorizer
	for _, a := range as {
		if a != nil {
			xs = append(xs, a)
		}
	}
	if len(xs) == 0 {
		return nil
	}
	if len(xs) == 1 {
		return xs[0]
	}
	return AuthorizerFunc(func(ctx context.Context, cx *Cx, args json.RawMessage) (Decision, error) {
		worst := Decision{Action: DecisionAllow}
		for _, a := range xs {
			d, err := a.Authorize(ctx, cx, args)
			if err != nil {
				return Decision{}, err
			}
			if severity(d.Action) > severity(worst.Action) {
				worst = d
			}
		}
		return worst, nil
	})
}

// severity 判定动作的严格程度（越大越严）。
func severity(a DecisionAction) int {
	switch a {
	case DecisionDeny:
		return 2
	case DecisionConfirm:
		return 1
	default:
		return 0
	}
}
