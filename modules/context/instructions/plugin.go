package instructions

// 插件登记（thin-kernel-plan §2.2）：AGENTS.md 系统块 + 嵌套指令 Transform 层（每会话一份，已装载集合属于本会话）。

import (
	"context"
	"github.com/Sevyn17/tenon/runtime/assemble"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/session"
)

const valueKey = "instructions"

type plug struct{}

func init() { component.Register("instructions", func() component.Component { return &plug{} }) }

func (*plug) Meta() component.Meta {
	return component.Meta{Name: "instructions", Version: "0.1.0", Default: true, Effects: []string{component.EffectSystemPrompt, component.EffectRewriteResult}, Requires: []string{assemble.ServiceWorkdir, "?" + assemble.ServiceTrust}}
}

func get(sc *assemble.SessionCx, root string, trusted bool) *Instructions {
	if in, ok := sc.Values[valueKey].(*Instructions); ok {
		return in
	}
	in := NewInstructions(root)
	in.Untrusted = !trusted
	if a := sc.Agent; a != nil {
		in.History = func() (int, []*session.Entry) {
			snap := a.Committed()
			off := snap.Header.CompactionBase - snap.Base
			if off < 0 || off > len(snap.Entries) {
				off = 0
			}
			return snap.Header.CompactionBase, snap.Entries[off:]
		}
	}
	sc.Values[valueKey] = in
	return in
}

func (*plug) Setup(cx *component.Registrar) error {
	root, err := component.Use[string](cx, assemble.ServiceWorkdir)
	if err != nil {
		return err
	}
	trusted := assemble.ProjectTrusted(cx) // 不受信项目：只读用户全局 AGENTS.md（审查 P1-4）
	// 04 4.3.1：不再登记「会话层」，改成 ToolCall 改写区（追加项目指令，超时由中间件自己实现）。
	// 每会话一份 Instructions 存在会话作用域上（req.Scope），与原来 LayerEntry.Func 每会话构造等价。
	component.Wrap(cx, component.ToolCall, func(ctx context.Context, req component.ToolCallReq,
		next func(context.Context, component.ToolCallReq) (component.ToolCallResp, error)) (component.ToolCallResp, error) {
		in := instructionsFor(req.Scope, root, trusted)
		return in.Middleware()(ctx, req, next)
	})
	component.RegisterAt(cx, assemble.SystemBlock, "instructions", assemble.BlockEntry{Order: 10,
		Func: func(sc *assemble.SessionCx) (*session.SystemBlock, error) { return get(sc, root, trusted).Block() }})
	return nil
}

func (*plug) Start(*component.Scope) error { return nil }

// instructionsFor 按会话作用域取本会话的 Instructions（没有就建一份存进去）。
// 等价于原来 LayerEntry.Func 里 get(sc, ...) 的「每会话一份」语义。
func instructionsFor(scope component.SessionScope, root string, trusted bool) *Instructions {
	if scope == nil {
		// 没有作用域（测试/装配期）：退化成一次性实例，不跨调用保留状态。
		in := NewInstructions(root)
		in.Untrusted = !trusted
		return in
	}
	if v, ok := scope.Value(valueKey); ok {
		if in, ok := v.(*Instructions); ok {
			return in
		}
	}
	in := NewInstructions(root)
	in.Untrusted = !trusted
	in.History = func() (int, []*session.Entry) { return scope.History() }
	scope.SetValue(valueKey, in)
	return in
}
