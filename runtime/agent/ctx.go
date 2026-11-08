package agent

// 调用上下文（框架设计 04 §4.4）：会话构造时建 Ctx，Open 时用会话行填全。

import (
	"github.com/Sevyn17/tenon/runtime/callctx"
	"github.com/Sevyn17/tenon/runtime/store"
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
}
