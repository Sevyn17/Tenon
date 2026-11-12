package component

// ToolCall 中间件点位（框架设计 04 4.3）。
//
// 与「会话层」（SessionLayer）的区别：会话层是**每会话构造时**取一组 `toolhub.Layer` 定义，
// 用于权限、沙箱、脱敏这类按会话配置的层；ToolCall 中间件是**每次调用时**包一层，
// 用于审计、限流、按调用改参数这类跨会话一致的逻辑。
//
// 落地方式：中间件链在最外层（`toolhub.Hub.PrependLayer` 语义），守卫仍在更内层——
// 「不能绕过守卫」由层顺序保证：中间件改写后的 args 会继续交给内层守卫重新判定。

import (
	"context"
	"encoding/json"

	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// PointToolCallMw 中间件点位名。
const PointToolCallMw = "tool_call"

// ToolCallReq 一次工具调用的请求（04 4.3）。
type ToolCallReq struct {
	// Tool 工具名。
	Tool string
	// Args 本次调用的参数（中间件可改写，改写后的参数仍要过守卫）。
	Args json.RawMessage
	// SessionID 会话 id。
	SessionID string
	// Risk 工具声明的风险级别。
	Risk toolhub.Risk
	// CallID 本次调用的 id。
	CallID string
	// Scope 本会话的作用域（04 4.3.1）：中间件要「每会话一份」的状态或历史回读时用它。
	// nil = 没有作用域（如测试直接构造）；中间件须容忍 nil。
	Scope SessionScope
}

// ToolCallResp 工具调用结果。
type ToolCallResp struct {
	// Output 工具输出（中间件可改写，如脱敏、截断）。
	Output toolhub.Output
	// Denied 是否被中间件拒绝（拒绝时不执行工具）。
	Denied bool
	// Reason 拒绝原因（Denied 为真时用；空则用「被中间件拒绝」）。进工具结果与 guard.denied。
	Reason string
}

// ToolCall 中间件点位。
var ToolCall = NewMwPoint[ToolCallReq, ToolCallResp](PointToolCallMw)

// ToolCallMw 便于组件书写的类型别名。
type ToolCallMw = Middleware[ToolCallReq, ToolCallResp]

// ToolCallTransforms 把 ToolCall 点位的中间件包成 toolhub.Transform（04 4.3.2 改写层）。
// 每个中间件一层，按登记顺序：先登记的在外层。没有中间件时返回 nil。
//
// 为什么是 Transform 不是 Gate：中间件能改参数/改结果（改写语义），而 Gate 是纯谓词。
// 中间件想「拒绝」时返回 Denied，由 Transform 适配层翻成失败结果——这是改写层唯一能做的
// 「软拒绝」，与 Gate 的硬拒绝（不执行、可 Confirm）不同。
func (a *Assembly) ToolCallTransforms() []toolhub.Transform {
	regs := a.middleware[PointToolCallMw]
	if len(regs) == 0 {
		return nil
	}
	out := make([]toolhub.Transform, 0, len(regs))
	for _, r := range regs {
		reg := r
		out = append(out, toolhub.Transform{Name: "mw:" + reg.plugin,
			Call: func(ctx context.Context, cx *toolhub.Cx, args json.RawMessage, next toolhub.Next) (toolhub.Output, error) {
				req := ToolCallReq{Tool: toolName(cx), Args: args, SessionID: cx.SessionID, Risk: riskOf(cx),
					CallID: cx.CallID, Scope: scopeOf(cx)}
				// 走通用的洋葱链：把 next 包成「调下一层」。
				out, err := reg.run(ctx, req, func(c context.Context, r any) (any, error) {
					rr, _ := r.(ToolCallReq)
					res, err := next(c, cx, rr.Args)
					return ToolCallResp{Output: res}, err
				})
				if err != nil {
					return toolhub.Output{}, err
				}
				resp, _ := out.(ToolCallResp)
				if resp.Denied {
					why := resp.Reason
					if why == "" {
						why = "被中间件拒绝"
					}
					return toolhub.Failf("%s", why), nil
				}
				return resp.Output, nil
			}})
	}
	return out
}

// toolName 取本次调用的工具名（cx.Tool 可能为空）。
func toolName(cx *toolhub.Cx) string {
	if cx == nil || cx.Tool == nil {
		return ""
	}
	return cx.Tool.Spec.Name
}

// riskOf 取本次调用工具的风险级别。
func riskOf(cx *toolhub.Cx) toolhub.Risk {
	if cx == nil || cx.Tool == nil {
		return toolhub.Read
	}
	return cx.Tool.Risk
}

// scopeOf 取会话作用域（由 assemble 在构造 Hub 时通过 toolhub.Cx 关联；没有则 nil）。
func scopeOf(cx *toolhub.Cx) SessionScope {
	if cx == nil || cx.Ctx == nil {
		return nil
	}
	if s, ok := cx.Ctx.Scope().(SessionScope); ok {
		return s
	}
	return nil
}
