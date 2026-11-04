package instructions

// 插件登记（thin-kernel-plan §2.2）：AGENTS.md 系统块 + 嵌套指令 Transform 层（每会话一份，已装载集合属于本会话）。

import (
	"ybase/runtime/assemble"
	"ybase/runtime/component"
	"ybase/runtime/session"
	"ybase/runtime/toolhub"
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
	cx.Register(assemble.PointSessionLayer, "instructions", assemble.LayerEntry{Order: 20,
		Func: func(sc *assemble.SessionCx) ([]toolhub.Layer, error) {
			return []toolhub.Layer{get(sc, root, trusted).Layer()}, nil
		}})
	cx.Register(assemble.PointSystemBlock, "instructions", assemble.BlockEntry{Order: 10,
		Func: func(sc *assemble.SessionCx) (*session.SystemBlock, error) { return get(sc, root, trusted).Block() }})
	return nil
}

func (*plug) Start(*component.Scope) error { return nil }
