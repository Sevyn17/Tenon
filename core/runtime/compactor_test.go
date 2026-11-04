package runtime

import (
	"context"
	"testing"

	"ybase-kernel/core/plugin"
	"ybase-kernel/core/reqlog"
	"ybase-kernel/core/session"
	"ybase-kernel/core/store/memstore"
)

type stubCompactor struct{}

func (stubCompactor) Plan(context.Context, string, *session.Core, bool) (*reqlog.CompactionView, map[string]any, error) {
	return nil, nil, nil
}

// compactorPlugin 提供 service:compactor 的测试组件。
type compactorPlugin struct{ v any }

func (p compactorPlugin) Meta() plugin.Meta {
	return plugin.Meta{Name: "cmp", Default: true, Provides: []string{ServiceCompactor}}
}
func (p compactorPlugin) Setup(cx *plugin.Registrar) error { return cx.Provide("compactor", p.v) }
func (compactorPlugin) Start(*plugin.Scope) error          { return nil }

func assembleWith(t *testing.T, p plugin.Plugin) *plugin.Assembly {
	t.Helper()
	asm, err := plugin.Assemble(plugin.Spec{Plugins: []plugin.Entry{{Name: "cmp"}},
		Instances: map[string]plugin.Plugin{"cmp": p}, Defaults: SlotDefaults()})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	return asm
}

// 装配里的压缩器经 Collect 交给 Builder，会话 agent 拿到它——自动压缩与超限强制压缩才会跑
// （2026-11-04 审查 P0-1：原先 Builder.Compactor 从没被赋值，只有手动 /compact 能用）。
func TestCollectCarriesCompactorToSessions(t *testing.T) {
	col, err := Collect(assembleWith(t, compactorPlugin{v: stubCompactor{}}))
	if err != nil {
		t.Fatal(err)
	}
	if col.Compactor == nil {
		t.Fatal("Collect 应取出 service:compactor")
	}
	b := &Builder{DB: memstore.New(), Layers: []LayerFunc{permLayer()}, Compactor: col.Compactor}
	a, _, err := b.Build("s1", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if a.Compactor == nil {
		t.Fatal("会话 agent 应带压缩器")
	}
	// 类型不符：装配期报错，不静默不压缩。
	if _, err := Collect(assembleWith(t, compactorPlugin{v: "not a compactor"})); err == nil {
		t.Fatal("service:compactor 类型不符应报错")
	}
}
