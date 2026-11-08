package assemble

import (
	"testing"

	"github.com/Sevyn17/tenon/runtime/callctx"
	"github.com/Sevyn17/tenon/runtime/inject"
	"github.com/Sevyn17/tenon/runtime/store/memstore"
)

// Builder.Statuses 透传到会话：插件登记的状态提交在调模型前被 agent 收集。
func TestBuilderCarriesStatuses(t *testing.T) {
	regs := []inject.Reg{{
		Key: "todo", Owner: "todo",
		Fn: func(*callctx.Ctx) []inject.Item { return nil },
	}}
	b := &Builder{
		DB:       memstore.New(),
		Layers:   []LayerFunc{permLayer()},
		Statuses: regs,
	}
	a, _, err := b.Build("s1", "", false)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(a.Statuses) != 1 || a.Statuses[0].Key != "todo" {
		t.Fatalf("状态登记未透传到 agent：%+v", a.Statuses)
	}
}
