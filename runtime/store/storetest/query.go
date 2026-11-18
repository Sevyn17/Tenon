package storetest

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/runtime/store"
)

// testPreview 检查点带的预览进会话行：Load 与列表都读得到；空串不覆盖已有值（客户端改进 M7 协议T9）。
func testPreview(t *testing.T, db store.Store) {
	ctx := context.Background()
	c := newCore()
	create(t, db, store.SessionMeta{ID: "s"}, c)
	_ = c.Record(txt("你好"), nil)
	n := commitAll(t, db, "s", c, 0, func(cp *store.Checkpoint) { cp.Preview = "第一版" })
	if l, err := db.Load(ctx, "s", nil); err != nil || l.Preview != "第一版" {
		t.Fatalf("Load 预览：%+v %v", l, err)
	}
	_ = c.Record(txt("再说"), nil)
	commitAll(t, db, "s", c, n, nil) // Preview 为空：保留原值
	if l, err := db.Load(ctx, "s", nil); err != nil || l.Preview != "第一版" {
		t.Fatalf("空预览不应覆盖：%+v %v", l, err)
	}
	if ls, err := db.ListSessions(ctx, 0, 10, false); err != nil || len(ls) != 1 || ls[0].Preview != "第一版" {
		t.Fatalf("列表预览：%+v %v", ls, err)
	}
}

// testSessionQuery 可选的 SessionQueryStore（客户端改进 M7 协议T12）：文本命中标题与任一条用户消息、% _ 按字面、
// 按 parent、归档三种取值、分页游标、工作区与未归属。
func testSessionQuery(t *testing.T, st store.Store) {
	ctx := context.Background()
	qs, ok := st.(store.SessionQueryStore)
	if !ok {
		t.Skip("存储未实现可选的 SessionQueryStore")
	}
	ms, ok := st.(store.SessionMetaStore)
	if !ok {
		t.Skip("归档要 SessionMetaStore")
	}
	// mk 建会话并依次记下用户消息；会话之间隔 2ms，updated_at 各不相同（顺序确定）。
	mk := func(m store.SessionMeta, users ...string) {
		t.Helper()
		c := newCore()
		create(t, st, m, c)
		for _, u := range users {
			if err := c.Record(txt(u), nil); err != nil {
				t.Fatal(err)
			}
		}
		if len(users) > 0 {
			commitAll(t, st, m.ID, c, 0, nil)
		}
		time.Sleep(2 * time.Millisecond)
	}
	mk(store.SessionMeta{ID: "t1", Title: "Alpha 计划", Workspace: "w"}, "随便聊聊")
	mk(store.SessionMeta{ID: "m2", Workspace: "w"}, "第一条", "第二条里有找到我")
	mk(store.SessionMeta{ID: "pct", Workspace: "w"}, "完成 100% 了")
	mk(store.SessionMeta{ID: "und", Workspace: "w"}, "a_b")
	mk(store.SessionMeta{ID: "plain", Workspace: "w"}, "abc 100 xyz")
	mk(store.SessionMeta{ID: "other", Workspace: "w2"}, "alpha 别的工作区")
	mk(store.SessionMeta{ID: "arch", Title: "alpha 已归档", Workspace: "w"})
	mk(store.SessionMeta{ID: "p", Workspace: "w"})
	mk(store.SessionMeta{ID: "p/c1", ParentID: "p", Origin: "subagent"}, "子任务 alpha")
	mk(store.SessionMeta{ID: "p/c2", ParentID: "p", Origin: "subagent"})
	mk(store.SessionMeta{ID: "none"}, "alpha 无归属")
	if err := ms.UpdateSessionMeta(ctx, "arch", nil, map[string]any{store.MetaArchived: true}); err != nil {
		t.Fatal(err)
	}

	list := func(q store.SessionQuery) []store.SessionInfo {
		t.Helper()
		ls, err := qs.QuerySessions(ctx, q)
		if err != nil {
			t.Fatalf("%+v：%v", q, err)
		}
		return ls
	}
	ids := func(q store.SessionQuery) []string {
		t.Helper()
		var out []string
		for _, x := range list(q) {
			out = append(out, x.ID)
		}
		slices.Sort(out)
		return out
	}
	want := func(name string, q store.SessionQuery, w ...string) {
		t.Helper()
		slices.Sort(w)
		if got := ids(q); !slices.Equal(got, w) {
			t.Errorf("%s：%v，应为 %v", name, got, w)
		}
	}

	want("标题命中，ASCII 不分大小写；排除已归档、别的工作区、子会话", store.SessionQuery{Workspace: "w", TopOnly: true, Text: "ALPHA"}, "t1")
	want("第二条用户消息命中", store.SessionQuery{Workspace: "w", Text: "找到我"}, "m2")
	want("% 按字面", store.SessionQuery{Workspace: "w", Text: "%"}, "pct")
	want("0% 按字面", store.SessionQuery{Workspace: "w", Text: "0%"}, "pct")
	want("_ 按字面", store.SessionQuery{Workspace: "w", Text: "_"}, "und")
	want(`\ 按字面`, store.SessionQuery{Workspace: "w", Text: `\`})
	want("不分工作区", store.SessionQuery{TopOnly: true, Text: "alpha"}, "t1", "other", "none")
	want("parent 的直接子会话", store.SessionQuery{Parent: "p"}, "p/c1", "p/c2")
	want("parent 与文本", store.SessionQuery{Parent: "p", Text: "alpha"}, "p/c1")
	want("未归属", store.SessionQuery{Unassigned: true, Text: "alpha"}, "none")
	want("未归属优先于 Workspace", store.SessionQuery{Unassigned: true, Workspace: "w"}, "none")

	// 归档三种取值。
	want("只看已归档", store.SessionQuery{Workspace: "w", TopOnly: true, Archived: store.ArchivedOnly}, "arch")
	excl := ids(store.SessionQuery{Workspace: "w", TopOnly: true})
	incl := ids(store.SessionQuery{Workspace: "w", TopOnly: true, Archived: store.ArchivedInclude})
	if slices.Contains(excl, "arch") || !slices.Contains(incl, "arch") || len(incl) != len(excl)+1 {
		t.Errorf("缺省排除已归档、include 都要：%v / %v", excl, incl)
	}
	if ls := list(store.SessionQuery{Workspace: "w", Archived: store.ArchivedOnly}); len(ls) != 1 || !store.ArchivedOf(ls[0].Meta) {
		t.Errorf("已归档的行带 archived 业务键：%+v", ls)
	}
	// 旧方法不看归档（与升级前一致；appapi 在存储实现了 SessionQueryStore 时改走 QuerySessions）。
	if ls, _ := st.ListSessionsIn(ctx, "w", 0, 50, true); !slices.ContainsFunc(ls, func(x store.SessionInfo) bool { return x.ID == "arch" }) {
		t.Errorf("ListSessionsIn 不应按归档过滤：%+v", ls)
	}
	// 恢复后回到缺省列表。
	if err := ms.UpdateSessionMeta(ctx, "arch", nil, map[string]any{store.MetaArchived: nil}); err != nil {
		t.Fatal(err)
	}
	if got := ids(store.SessionQuery{Workspace: "w", TopOnly: true}); !slices.Contains(got, "arch") {
		t.Errorf("恢复后缺省列表应有 arch：%v", got)
	}

	// 分页：按 updated_at 倒序，游标是上一页最后一行的 updated_at，拼起来等于一次取全。
	all := list(store.SessionQuery{Workspace: "w", TopOnly: true, Limit: 50})
	var paged []store.SessionInfo
	var before int64
	for range 20 {
		pg := list(store.SessionQuery{Workspace: "w", TopOnly: true, Limit: 2, Before: before})
		paged = append(paged, pg...)
		if len(pg) < 2 {
			break
		}
		before = pg[len(pg)-1].UpdatedAt
	}
	same := len(all) == len(paged) && len(all) == 7
	for i := 0; same && i < len(all); i++ {
		same = all[i].ID == paged[i].ID
	}
	if !same {
		t.Errorf("分页拼起来应等于一次取全（7 条）：%v / %v", infoIDs(all), infoIDs(paged))
	}
	for i := 1; i < len(all); i++ {
		if all[i-1].UpdatedAt < all[i].UpdatedAt {
			t.Errorf("应按 updated_at 倒序：%v", all)
		}
	}
	// 分页同样适用于文本搜索。
	if pg := list(store.SessionQuery{TopOnly: true, Text: "alpha", Limit: 2}); len(pg) != 2 ||
		len(list(store.SessionQuery{TopOnly: true, Text: "alpha", Limit: 2, Before: pg[1].UpdatedAt})) != 2 {
		t.Errorf("文本搜索分页：%v", infoIDs(pg))
	}
}

func infoIDs(ls []store.SessionInfo) []string {
	out := make([]string, len(ls))
	for i, x := range ls {
		out[i] = x.ID
	}
	return out
}
