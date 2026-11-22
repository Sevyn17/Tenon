package tenon_test

import (
	"context"
	"testing"

	"github.com/Sevyn17/tenon"
	"github.com/Sevyn17/tenon/runtime/agentdef"
	"github.com/Sevyn17/tenon/runtime/store"
)

// T-3：WithStoreAgents + WithDefaultAgent。
//   - 新建顶层会话不给定义名时用缺省定义；
//   - 租户层同名副本遮蔽平台定义，新会话拿到副本；
//   - 副本写墓碑后回落到平台定义；
//   - 已有会话重开不受缺省定义影响。
func TestStoreAgentsAndDefaultAgent(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeAgent(t, dir, "main", "format = \"tenon/v1\"\nid = \"main\"\ndescription = \"内置\"\n")
	opts := append(base(t), tenon.WithAgents(tenon.Dir(dir)), tenon.WithStoreAgents(), tenon.WithDefaultAgent("main"))
	app, err := tenon.New(opts...)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer app.Close(ctx)
	in := app.Internals()
	h, db := in.Host, in.DB

	meta := func(id string) store.SessionMeta {
		t.Helper()
		l, err := db.Load(ctx, id, nil)
		if err != nil {
			t.Fatal(err)
		}
		return l.Meta
	}
	create := func(id string) store.SessionMeta {
		t.Helper()
		if _, err := h.Create(ctx, id, "", nil); err != nil {
			t.Fatalf("Create %s: %v", id, err)
		}
		return meta(id)
	}

	m1 := create("s1")
	if m1.AgentID != "main" || m1.AgentHash == "" {
		t.Fatalf("缺省定义未生效：%q %q", m1.AgentID, m1.AgentHash)
	}

	src := app.StoreAgents()
	if src == nil {
		t.Fatal("WithStoreAgents 时 StoreAgents 不应为 nil")
	}
	ref := agentdef.Ref{Namespace: agentdef.Tenant, Tenant: store.Tenant, Name: "main"}
	_, copyHash, err := src.Save(ctx, ref, agentdef.Files{agentdef.ManifestName: []byte("format = \"tenon/v1\"\nid = \"main\"\ndescription = \"副本\"\n")})
	if err != nil {
		t.Fatal(err)
	}
	if m2 := create("s2"); m2.AgentHash != copyHash || m2.AgentHash == m1.AgentHash {
		t.Fatalf("副本应遮蔽平台定义：%q，副本 %q", m2.AgentHash, copyHash)
	}

	if _, err := src.Delete(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if m3 := create("s3"); m3.AgentHash != m1.AgentHash {
		t.Fatalf("墓碑后应回到平台定义：%q，期望 %q", m3.AgentHash, m1.AgentHash)
	}
	if a, err := src.ByHash(ctx, copyHash); err != nil || a.Description != "副本" {
		t.Fatalf("按哈希还原副本：%v %+v", err, a.Description)
	}

	// 已有会话：会话行不变。
	if m := meta("s1"); m.AgentID != "main" || m.AgentHash != m1.AgentHash {
		t.Fatalf("已有会话被改：%+v", m)
	}
}
