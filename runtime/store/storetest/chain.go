package storetest

import (
	"context"
	"slices"
	"testing"

	"github.com/Sevyn17/tenon/runtime/store"
)

// testChain 创建链字段（Mortise docs/76 C1）：CreatorSession / ChainDepth / ChainRoot 在 Load 与列表里往返；
// 未记的旧行按第一层、根为自身；SessionQueryStore 能按创建者筛选。
func testChain(t *testing.T, db store.Store) {
	ctx := context.Background()
	create(t, db, store.SessionMeta{Workspace: "w", ID: "r"}, newCore())
	create(t, db, store.SessionMeta{ID: "r/sub-1", ParentID: "r", Origin: "subagent",
		CreatorSession: "r", ChainDepth: 2, ChainRoot: "r"}, newCore())
	create(t, db, store.SessionMeta{Workspace: "w", ID: "ind", Origin: "agent",
		CreatorSession: "r", ChainDepth: 2, ChainRoot: "r"}, newCore())
	create(t, db, store.SessionMeta{Workspace: "w", ID: "other"}, newCore())

	l, err := db.Load(ctx, "ind", nil)
	if err != nil {
		t.Fatal(err)
	}
	if l.Meta.CreatorSession != "r" || l.Meta.ChainDepth != 2 || l.Meta.ChainRoot != "r" || l.Meta.ParentID != "" {
		t.Fatalf("独立会话的创建链字段：%+v", l.Meta)
	}
	if l.Meta.Depth() != 2 || l.Meta.Root() != "r" {
		t.Fatalf("Depth/Root：%d %s", l.Meta.Depth(), l.Meta.Root())
	}
	root, err := db.Load(ctx, "r", nil)
	if err != nil {
		t.Fatal(err)
	}
	if root.Meta.CreatorSession != "" || root.Meta.Depth() != 1 || root.Meta.Root() != "r" {
		t.Fatalf("第一层会话：%+v depth=%d root=%s", root.Meta, root.Meta.Depth(), root.Meta.Root())
	}

	ls, err := db.ListSessions(ctx, 0, 50, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range ls {
		if s.ID == "r/sub-1" && (s.CreatorSession != "r" || s.ChainDepth != 2 || s.ChainRoot != "r") {
			t.Fatalf("列表行应带创建链：%+v", s)
		}
	}

	qs, ok := db.(store.SessionQueryStore)
	if !ok {
		return
	}
	got, err := qs.QuerySessions(ctx, store.SessionQuery{CreatorSession: "r"})
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, s := range got {
		ids = append(ids, s.ID)
	}
	slices.Sort(ids)
	if !slices.Equal(ids, []string{"ind", "r/sub-1"}) {
		t.Fatalf("按创建者筛选：%v，应为 [ind r/sub-1]", ids)
	}
	if got, _ := qs.QuerySessions(ctx, store.SessionQuery{CreatorSession: "r", TopOnly: true}); len(got) != 1 || got[0].ID != "ind" {
		t.Fatalf("按创建者且只要顶层：%+v，应只有 ind", got)
	}
	// ExcludeOrigins：按来源排除（Mortise docs/77 B1），可多个，与其他条件为「且」。
	create(t, db, store.SessionMeta{Workspace: "w", ID: "rev", Origin: "review"}, newCore())
	qids := func(q store.SessionQuery) []string {
		t.Helper()
		got, err := qs.QuerySessions(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, s := range got {
			out = append(out, s.ID)
		}
		slices.Sort(out)
		return out
	}
	if got := qids(store.SessionQuery{Workspace: "w", TopOnly: true}); !slices.Equal(got, []string{"ind", "other", "r", "rev"}) {
		t.Fatalf("不排除时：%v", got)
	}
	if got := qids(store.SessionQuery{Workspace: "w", TopOnly: true, ExcludeOrigins: []string{"review"}}); !slices.Equal(got, []string{"ind", "other", "r"}) {
		t.Fatalf("排除 review：%v", got)
	}
	if got := qids(store.SessionQuery{Workspace: "w", ExcludeOrigins: []string{"review", "agent"}}); !slices.Equal(got, []string{"other", "r", "r/sub-1"}) {
		t.Fatalf("排除 review、agent：%v", got)
	}
}
