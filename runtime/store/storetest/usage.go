package storetest

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/runtime/store"
)

// testUsageSummary 可选的 UsageSummarizer（Mortise docs/74 W1b）：
// 按（定义名，main/child）合计 turn 与 attempt span；同一 span 再提交不重复计；since 之前的天不计；
// 未绑定定义的会话不计；删除会话后它的用量消失；agents 过滤生效。
func testUsageSummary(t *testing.T, st store.Store) {
	us, ok := st.(store.UsageSummarizer)
	if !ok {
		t.Skip("存储未实现可选的 UsageSummarizer")
	}
	ctx := context.Background()
	now := time.Now()
	old := now.AddDate(0, 0, -40)
	attempt := func(id string, at time.Time, in, out uint64) store.Span {
		b, _ := json.Marshal(map[string]any{"input": in, "output": out, "cache_read": 1, "cache_write": 2, "usage_source": "Reported"})
		return store.Span{ID: id, Kind: "attempt", Status: "ok", Start: at, End: at, Attrs: b}
	}
	turn := func(id string, at time.Time) store.Span {
		return store.Span{ID: id, Kind: "turn", Status: "done", Start: at, End: at}
	}
	commit := func(id string, spans ...store.Span) {
		t.Helper()
		l, err := st.Load(ctx, id, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Commit(ctx, store.Checkpoint{SessionID: id, ExpectLogLen: l.Snapshot.Header.LogLen, AppendFrom: l.Snapshot.Header.LogLen,
			Header: l.Snapshot.Header, Spans: spans}); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range []store.SessionMeta{
		{ID: "u1", AgentID: "main", Workspace: "w"},
		{ID: "u2", AgentID: "main", Workspace: "w2"},
		{ID: "u1/c", AgentID: "helper", ParentID: "u1", Origin: "subagent"},
		{ID: "u3", AgentID: "reviewer", Workspace: "w"},
		{ID: "u4", Workspace: "w"}, // 未绑定定义
		{ID: "u5", AgentID: "main", Workspace: "w"},
	} {
		create(t, st, m, newCore())
	}
	a1 := attempt("ua1", now, 100, 10)
	commit("u1", turn("ut1", now), a1, attempt("ua0", old, 999, 999), store.Span{ID: "us1", Kind: "step", Start: now})
	commit("u1", a1) // 覆盖更新不重复计
	commit("u2", turn("ut2", now), attempt("ua2", now, 50, 5))
	commit("u1/c", turn("ut3", now), attempt("ua3", now, 7, 1))
	commit("u3", turn("ut4", now), attempt("ua4", now, 3, 3))
	commit("u4", turn("ut5", now), attempt("ua5", now, 1000, 1000))
	commit("u5", turn("ut6", now), attempt("ua6", now, 1, 1))
	if err := st.DeleteSession(ctx, "u5", nil); err != nil {
		t.Fatal(err)
	}

	since := store.UsageDay(now.AddDate(0, 0, -29))
	rows, err := us.UsageSummary(ctx, since, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]store.UsageRow{}
	for _, r := range rows {
		got[r.Agent+"/"+r.Scope] = r
	}
	if len(got) != 3 {
		t.Fatalf("应有 main/main、helper/child、reviewer/main 三行：%+v", rows)
	}
	m := got["main/main"]
	if m.Sessions != 2 || m.Turns != 2 || m.Attempts != 2 || m.Input != 150 || m.Output != 15 || m.CacheRead != 2 || m.CacheWrite != 4 {
		t.Fatalf("main/main = %+v", m)
	}
	if c := got["helper/child"]; c.Sessions != 1 || c.Turns != 1 || c.Input != 7 {
		t.Fatalf("helper/child = %+v", c)
	}
	if r := got["reviewer/main"]; r.Sessions != 1 || r.Input != 3 {
		t.Fatalf("reviewer/main = %+v", r)
	}
	all, err := us.UsageSummary(ctx, "", []string{"main"})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Input != 150+999 || all[0].Attempts != 3 {
		t.Fatalf("since 空 + agents 过滤：%+v", all)
	}
}
