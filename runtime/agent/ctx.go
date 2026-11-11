package agent

// 调用上下文（框架设计 04 §4.4）：会话构造时建 Ctx，Open 时用会话行填全。

import (
	"context"

	"github.com/Sevyn17/tenon/runtime/callctx"
	"github.com/Sevyn17/tenon/runtime/store"
	"github.com/Sevyn17/tenon/runtime/useconf"
)

// toolCtx 本次工具调用与层用的上下文；没配时按会话 id 兜底（测试与嵌入方）。
func (a *Agent) toolCtx() *callctx.Ctx {
	if a.Ctx != nil {
		return a.Ctx
	}
	return callctx.New(callctx.Options{SessionID: a.SessionID})
}

// bindCtx 用会话行填 Ctx：定义名与哈希、创建者、父会话、会话键、业务 meta、使用配置。
// 子会话标记由构造方决定，Bind 不会覆盖。
func (a *Agent) bindCtx(m store.SessionMeta) {
	if a.Ctx == nil {
		return
	}
	a.Ctx.Bind(callctx.Session{
		ID:         a.SessionID,
		Definition: m.AgentID,
		Hash:       m.AgentHash,
		Creator:    m.Creator,
		Parent:     m.ParentID,
		Key:        m.SessionKey,
	}, m.Meta, m.UseConfig)
	// 按本轮触发者重解使用配置的能力（T4f，08 8.3）：守卫每轮按触发者的用户层上限取一次。
	// 只有配了租户/用户来源、且有定义时才装；否则 UseForTrigger 退回会话算好的那份。
	if a.UseSource != nil && a.Def != nil && len(a.Schemas) > 0 {
		schemas, defUse := a.Schemas, a.Def.Use
		a.Ctx.SetReResolver(func(user, component string) (map[string]any, bool) {
			vals, err := useconf.ResolveAll(context.Background(), schemas, defUse, a.UseSource, store.Tenant, user)
			if err != nil {
				return nil, false
			}
			v, ok := vals[component]
			return v, ok
		})
	}
}
