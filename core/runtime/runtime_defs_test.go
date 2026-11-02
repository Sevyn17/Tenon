package runtime

import (
	"context"
	"strings"
	"testing"

	"ybase-kernel/core/agentdef"
	"ybase-kernel/core/session"
	"ybase-kernel/core/store"
	"ybase-kernel/core/store/memstore"
)

// defsSource 造一个内存定义来源（平台命名空间，最小 manifest）。
func defsSource(t *testing.T, names ...string) agentdef.Source {
	t.Helper()
	m := agentdef.Mem()
	for _, name := range names {
		files := agentdef.Files{"agent.toml": []byte(
			"format = \"ybase/v1\"\nid = \"" + name + "\"\ndescription = \"测试定义\"\n")}
		if err := m.Put(agentdef.Ref{Namespace: agentdef.Platform, Name: name}, files); err != nil {
			t.Fatalf("Put %s：%v", name, err)
		}
	}
	return m
}

func TestBuildWithDefinition(t *testing.T) {
	b := &Builder{
		DB:          memstore.New(),
		Layers:      []LayerFunc{permLayer()},
		Definitions: defsSource(t, "subagent"),
	}
	a, _, err := b.Build("s1", "subagent", true)
	if err != nil {
		t.Fatalf("Build 带定义：%v", err)
	}
	if a.Def == nil || a.Def.ID != "subagent" {
		t.Fatalf("a.Def = %+v，期望 subagent", a.Def)
	}
	if a.Def.Version == "" {
		t.Fatal("定义版本应已算出")
	}

	// 不指定定义名的会话（顶层会话）保持无定义，行为与之前一致。
	a2, _, err := b.Build("s2", "", false)
	if err != nil {
		t.Fatalf("Build 无定义：%v", err)
	}
	if a2.Def != nil {
		t.Fatalf("顶层会话不应带定义，实际 %q", a2.Def.ID)
	}

	// 未知名定义 → 报错。
	if _, _, err := b.Build("s3", "nope", true); err == nil {
		t.Fatal("未知定义名应报错")
	}

	// 没有定义来源时，指定了定义名 → 报错；不指定 → 正常。
	bare := &Builder{DB: memstore.New(), Layers: []LayerFunc{permLayer()}}
	if _, _, err := bare.Build("s4", "subagent", true); err == nil {
		t.Fatal("没有定义来源时应报错")
	}
	if _, _, err := bare.Build("s5", "", false); err != nil {
		t.Fatalf("没有定义来源、不指定定义：%v", err)
	}
}

// 重启恢复时，定义名从会话行的 AgentID 取回（不靠调用方记得）。
func TestBuildRestoresDefinitionFromSession(t *testing.T) {
	ctx := context.Background()
	db := memstore.New()
	core := session.New(&session.Config{Model: "m", Policy: session.DefaultPolicy()})
	meta := store.SessionMeta{ID: "r1", Origin: "subagent", ParentID: "p", AgentID: "subagent", AgentHash: "h"}
	if err := db.CreateSession(ctx, meta, core.Snapshot().Header, nil); err != nil {
		t.Fatalf("CreateSession：%v", err)
	}
	b := &Builder{
		DB:          db,
		Layers:      []LayerFunc{permLayer()},
		Definitions: defsSource(t, "subagent"),
	}
	a, _, err := b.Build("r1", "", true)
	if err != nil {
		t.Fatalf("Build 恢复：%v", err)
	}
	if a.Def == nil || a.Def.ID != "subagent" {
		t.Fatalf("恢复后 a.Def = %+v，期望 subagent", a.Def)
	}

	// 定义来源里没有这份定义 → 恢复时报错，而不是静默降级成无定义。
	b.Definitions = defsSource(t)
	if _, _, err := b.Build("r1", "", true); err == nil || !strings.Contains(err.Error(), "定义") {
		t.Fatalf("定义缺失时应报错，实际 err=%v", err)
	}
}
