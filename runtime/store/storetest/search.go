package storetest

import (
	"context"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
)

// testEntrySearch 可选的 EntrySearcher 与 ArchiveReader（Mortise docs/76 §16，session_turns {query}）：
// 三类条目（用户、助手、工具结果）都能搜到，按下标倒序，limit 生效，kinds 过滤生效；
// 回退后被截掉的条目不再出现在搜索里，而是出现在归档里（带 rewind_seq）。
func testEntrySearch(t *testing.T, st store.Store) {
	es, ok := st.(store.EntrySearcher)
	if !ok {
		t.Skip("存储未实现可选的 EntrySearcher")
	}
	ctx := context.Background()
	c := newCore()
	create(t, st, store.SessionMeta{ID: "sr"}, c)
	// 回合 1：用户提到 alpha；回合 2：助手调用 echo，工具结果里有 alpha；回合 3：用户提到 beta。
	n, err := c.Input(txt("请处理 alpha 任务"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ModelDone(n.Model.RequestID, n.ID, session.AssistantItem{Content: txt("好的")}, session.StopReason{V: "stop"}); err != nil {
		t.Fatal(err)
	}
	n, err = c.Input(txt("再跑一次"), nil)
	if err != nil {
		t.Fatal(err)
	}
	n2, err := c.ModelDone(n.Model.RequestID, n.ID, session.AssistantItem{Content: txt("调用工具"),
		ToolCalls: []session.ToolCall{{ID: "call-1", Name: "echo", Args: []byte(`{"q":"x"}`)}}}, session.StopReason{V: "tool_use"})
	if err != nil {
		t.Fatal(err)
	}
	n3, err := c.SubmitResult("call-1", txt("输出含 ALPHA 结果"), session.ToolOK)
	if err != nil {
		t.Fatal(err)
	}
	_ = n2
	if _, err := c.ModelDone(n3.Model.RequestID, n3.ID, session.AssistantItem{Content: txt("完成")}, session.StopReason{V: "stop"}); err != nil {
		t.Fatal(err)
	}
	n, err = c.Input(txt("beta 方案"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ModelDone(n.Model.RequestID, n.ID, session.AssistantItem{Content: txt("收到 beta")}, session.StopReason{V: "stop"}); err != nil {
		t.Fatal(err)
	}
	logLen := commitAll(t, st, "sr", c, 0, nil)

	hits, err := es.SearchEntries(ctx, "sr", "alpha", nil, 20)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for i, h := range hits {
		if i > 0 && h.Idx >= hits[i-1].Idx {
			t.Fatalf("应按下标倒序：%+v", hits)
		}
		if !strings.Contains(strings.ToLower(store.EntryText(h.Entry)), "alpha") {
			t.Fatalf("命中条目文本不含 alpha：%q", store.EntryText(h.Entry))
		}
		kinds = append(kinds, h.Entry.Item.Kind)
	}
	if len(hits) != 2 || kinds[0] != session.KindToolResult || kinds[1] != session.KindUser {
		t.Fatalf("应命中工具结果与用户各一条（不区分大小写）：%v", kinds)
	}
	if hits, _ := es.SearchEntries(ctx, "sr", "alpha", []string{session.KindUser}, 20); len(hits) != 1 || hits[0].Entry.Item.Kind != session.KindUser {
		t.Fatalf("kinds 过滤应只剩用户条目：%+v", hits)
	}
	if hits, _ := es.SearchEntries(ctx, "sr", "beta", nil, 1); len(hits) != 1 || hits[0].Entry.Item.Kind != session.KindAssistant {
		t.Fatalf("limit=1 应只返回最新一条（助手）：%+v", hits)
	}
	if hits, _ := es.SearchEntries(ctx, "sr", "100%_", nil, 20); len(hits) != 0 {
		t.Fatalf("通配字符应按字面匹配：%+v", hits)
	}
	if hits, err := es.SearchEntries(ctx, "missing", "alpha", nil, 20); err != nil || len(hits) != 0 {
		t.Fatalf("未知会话应返回空：%v %v", hits, err)
	}

	// 回退第 3 回合：beta 不再出现在搜索里，进归档。
	to := -1
	for i, e := range c.Snapshot().Entries {
		if e.Turn == 3 {
			to = i
			break
		}
	}
	if to < 0 {
		t.Fatal("没有第 3 回合")
	}
	if err := c.Rewind(to); err != nil {
		t.Fatal(err)
	}
	s := c.Snapshot()
	if err := st.Commit(ctx, store.Checkpoint{SessionID: "sr", ExpectLogLen: logLen, TruncateFrom: &to, AppendFrom: to,
		Header: s.Header, RunState: "idle"}); err != nil {
		t.Fatal(err)
	}
	if hits, _ := es.SearchEntries(ctx, "sr", "beta", nil, 20); len(hits) != 0 {
		t.Fatalf("已回退条目不应出现在搜索里：%+v", hits)
	}
	ar, ok := st.(store.ArchiveReader)
	if !ok {
		return
	}
	arch, err := ar.ArchivedEntries(ctx, "sr")
	if err != nil {
		t.Fatal(err)
	}
	if len(arch) != 2 || arch[0].Entry.Turn != 3 || arch[0].RewindSeq == 0 || arch[0].Idx != to || arch[1].Idx != to+1 {
		t.Fatalf("归档应有第 3 回合两条，按下标升序、带 rewind_seq：%+v", arch)
	}
	if a, err := ar.ArchivedEntries(ctx, "missing"); err != nil || len(a) != 0 {
		t.Fatalf("未知会话归档应为空：%v %v", a, err)
	}
}
