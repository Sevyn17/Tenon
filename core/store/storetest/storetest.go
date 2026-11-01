// Package storetest：store.Store 一致性测试（T1a）。sqlite 与 memstore 共用，语义以 sqlite 为准。
//
// 用法：storetest.Run(t, func(t *testing.T) store.Store { ... })；open 每次返回一个全新的空存储，
// 关闭由 open 自行登记到 t.Cleanup。只走接口，不依赖实现专有能力（SQL 等）。
package storetest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"ybase-kernel/core/bus"
	"ybase-kernel/core/session"
	"ybase-kernel/core/store"
)

// Run 跑全部一致性用例。
func Run(t *testing.T, open func(t *testing.T) store.Store) {
	cases := []struct {
		name string
		f    func(*testing.T, store.Store)
	}{
		{"RoundTripAndRestore", testRoundTrip},
		{"ConditionalWrite", testConditionalWrite},
		{"TruncateArchivesAndSyncSeq", testTruncate},
		{"ReplayListAndNotify", testReplayAndList},
		{"Definitions", testDefinitions},
		{"InboxIdempotentAcrossConsume", testInbox},
		{"ToolStateMachine", testToolStateMachine},
		{"ConfirmLifecycle", testConfirmLifecycle},
		{"PendConfirmRefreshesHash", testPendConfirmRefresh},
		{"DeleteCleanersAndTombstone", testDelete},
		{"SessionMetaRoundTrip", testMetaRoundTrip},
		{"SessionKeyUnique", testSessionKeyUnique},
		{"Stats", testStats},
		{"Export", testExport},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.f(t, open(t)) })
	}
}

// —— 辅助 ——//

func txt(s string) session.Content { return session.Content{session.NewTextPart(s)} }

func newCore() *session.Core {
	return session.New(&session.Config{Model: "m", System: []session.SystemBlock{{Text: "sys", Source: "agent"}},
		Tools: []session.ToolSpec{{Name: "echo", Schema: json.RawMessage(`{"type":"object"}`)}}, Policy: session.DefaultPolicy()})
}

// commitAll 把内核当前快照中 [expect, end) 的条目随头部提交，返回新 log_len。
func commitAll(t *testing.T, db store.Store, id string, c *session.Core, expect int, extra func(*store.Checkpoint)) int {
	t.Helper()
	s := c.Snapshot()
	cp := store.Checkpoint{SessionID: id, ExpectLogLen: expect, AppendFrom: expect,
		Append: s.Entries[expect-s.Base:], Header: s.Header, RunState: "running"}
	if extra != nil {
		extra(&cp)
	}
	if err := db.Commit(context.Background(), cp); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	return s.Header.LogLen
}

func create(t *testing.T, db store.Store, m store.SessionMeta, c *session.Core) {
	t.Helper()
	if err := db.CreateSession(context.Background(), m, c.Snapshot().Header, nil); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
}

// —— 会话 ——//

func testRoundTrip(t *testing.T, db store.Store) {
	ctx := context.Background()
	c := newCore()
	create(t, db, store.SessionMeta{ID: "s1"}, c)
	if ok, err := db.Exists(ctx, "s1"); err != nil || !ok {
		t.Fatalf("Exists: %v %v", ok, err)
	}
	if ok, _ := db.Exists(ctx, "none"); ok {
		t.Fatal("不存在的会话 Exists 应为 false")
	}
	_, n1, _ := c.Input(txt("你好"), nil)
	n := commitAll(t, db, "s1", c, 0, func(cp *store.Checkpoint) {
		cp.Request = &store.RequestRecord{RequestID: n1.Model.RequestID, ShellHash: "x", ShellBody: []byte(`{"a":1}`),
			PrefixEnd: 1, FinalHash: "f", Composition: json.RawMessage(`{"system":3}`)}
		cp.Spans = []store.Span{{ID: "sp1", Kind: "step", Start: time.Now()}}
		cp.Preview = "你好"
	})
	_, _, _ = c.ModelDone(n1.Model.RequestID, n1.ID, session.AssistantItem{Content: txt("嗨")}, session.StopReason{})
	commitAll(t, db, "s1", c, n, nil)

	l, err := db.Load(ctx, "s1", nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if l.Snapshot.Header.Config == nil || l.Snapshot.Header.Config.System[0].Source != "agent" {
		t.Fatalf("外壳未还原: %+v", l.Snapshot.Header.Config)
	}
	if len(l.Snapshot.Entries) != 2 || l.RunState != "running" || l.Meta.Origin != "new" {
		t.Fatalf("条目 %d run=%q origin=%q", len(l.Snapshot.Entries), l.RunState, l.Meta.Origin)
	}
	if _, err := session.Restore(l.Snapshot); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	r, err := db.GetRequest(ctx, "s1", n1.Model.RequestID)
	if err != nil || r == nil || r.FinalHash != "f" || string(r.Composition) != `{"system":3}` || r.PrefixEnd != 1 {
		t.Fatalf("请求记录: %+v %v", r, err)
	}
	if r, err := db.GetRequest(ctx, "s1", "none"); r != nil || err != nil {
		t.Fatalf("缺失请求应 (nil, nil): %+v %v", r, err)
	}
	if b, err := db.ShellBody(ctx, "x"); err != nil || string(b) != `{"a":1}` {
		t.Fatalf("请求外壳: %s %v", b, err)
	}
	if _, err := db.ShellBody(ctx, "nope"); err == nil {
		t.Fatal("缺失外壳应报错")
	}
	h, err := store.ShellHash(l.Snapshot.Header.Config)
	if err != nil {
		t.Fatal(err)
	}
	if cfg, err := db.Shell(ctx, h); err != nil || cfg.Model != "m" {
		t.Fatalf("Shell: %+v %v", cfg, err)
	}
	es, err := db.Entries(ctx, "s1", 1, -1)
	if err != nil || len(es) != 1 {
		t.Fatalf("Entries[1,): %d %v", len(es), err)
	}
	if es, _ := db.Entries(ctx, "s1", 0, 1); len(es) != 1 {
		t.Fatalf("Entries[0,1): %d", len(es))
	}
	one := 1
	if l, err := db.Load(ctx, "s1", &one); err != nil || l.Snapshot.Base != 1 || len(l.Snapshot.Entries) != 1 {
		t.Fatalf("Load base=1: %+v %v", l, err)
	}
	if _, err := db.Load(ctx, "none", nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("缺失会话 Load: %v", err)
	}
	if err := db.Commit(ctx, store.Checkpoint{SessionID: "none"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("缺失会话 Commit: %v", err)
	}
	if err := db.CreateSession(ctx, store.SessionMeta{ID: "s1"}, c.Snapshot().Header, nil); err == nil {
		t.Fatal("重复 id 应报错")
	}
	ls, _ := db.ListSessions(ctx, 0, 10, false)
	if len(ls) != 1 || ls[0].Preview != "你好" {
		t.Fatalf("预览: %+v", ls)
	}
	// 初始条目：log_len = 条目数
	c2 := newCore()
	c2.Record(txt("a"), nil)
	s2 := c2.Snapshot()
	if err := db.CreateSession(ctx, store.SessionMeta{ID: "s2"}, s2.Header, s2.Entries); err != nil {
		t.Fatal(err)
	}
	if err := db.Commit(ctx, store.Checkpoint{SessionID: "s2", ExpectLogLen: len(s2.Entries), Header: s2.Header}); err != nil {
		t.Fatalf("初始条目后 log_len 应为 %d: %v", len(s2.Entries), err)
	}
}

func testConditionalWrite(t *testing.T, db store.Store) {
	ctx := context.Background()
	c := newCore()
	create(t, db, store.SessionMeta{ID: "s"}, c)
	c.Input(txt("a"), nil)
	n := commitAll(t, db, "s", c, 0, nil)
	var got []bus.Envelope
	db.OnCommit(func(e bus.Envelope) { got = append(got, e) })
	defer db.OnCommit(nil)
	s := c.Snapshot()
	err := db.Commit(ctx, store.Checkpoint{SessionID: "s", ExpectLogLen: 0, Header: s.Header, Preview: "x",
		Spans: []store.Span{{ID: "x1", Kind: "tool", Start: time.Now()}}})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("应冲突: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("冲突不应通知: %+v", got)
	}
	if st, _ := db.SessionStats(ctx, "s"); st.ToolCalls != 0 {
		t.Fatal("冲突提交不应落 span")
	}
	l, _ := db.Load(ctx, "s", nil)
	if l.Snapshot.Header.LogLen != n || len(l.Snapshot.Entries) != n {
		t.Fatalf("冲突后状态被改: %+v", l.Snapshot.Header)
	}
}

func testTruncate(t *testing.T, db store.Store) {
	ctx := context.Background()
	c := newCore()
	create(t, db, store.SessionMeta{ID: "s"}, c)
	c.Record(txt("a"), nil)
	c.Record(txt("b"), nil)
	n := commitAll(t, db, "s", c, 0, nil)
	if _, err := c.Rewind(1); err != nil {
		t.Fatalf("Rewind: %v", err)
	}
	c.Record(txt("c"), nil)
	from := 1
	s := c.Snapshot()
	if err := db.Commit(ctx, store.Checkpoint{SessionID: "s", ExpectLogLen: n, TruncateFrom: &from,
		AppendFrom: 1, Append: s.Entries[1:], Header: s.Header}); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	// a=1, b=2，截断占 3，c=4
	evs, wm, err := db.Replay(ctx, "s", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 3 || wm != 4 {
		t.Fatalf("回放 %+v wm=%d", evs, wm)
	}
	if evs[0].Seq != 1 || evs[1].Seq != 3 || evs[1].Kind != store.EventRewind || evs[2].Seq != 4 || evs[2].Kind != store.EventEntry {
		t.Fatalf("seq 序列: %+v", evs)
	}
	if to := evs[1].Data.(map[string]any)["to"]; to != 1 {
		t.Fatalf("rewind to = %v", to)
	}
	l, _ := db.Load(ctx, "s", nil)
	if len(l.Snapshot.Entries) != 2 || l.Snapshot.Entries[1].Item.User == nil {
		t.Fatalf("截断后条目: %d", len(l.Snapshot.Entries))
	}
	// 归档可经导出看到
	var buf bytes.Buffer
	if err := db.Export(ctx, "s", &buf); err != nil {
		t.Fatal(err)
	}
	if k := exportKinds(t, &buf); k["archived"] != 1 || k["entry"] != 2 {
		t.Fatalf("导出 %v", k)
	}
}

func testReplayAndList(t *testing.T, db store.Store) {
	ctx := context.Background()
	c := newCore()
	create(t, db, store.SessionMeta{ID: "s1", Title: "一"}, c)
	_, n1, _ := c.Input(txt("你好"), nil)
	n := commitAll(t, db, "s1", c, 0, nil)
	_, _, _ = c.ModelDone(n1.Model.RequestID, n1.ID, session.AssistantItem{Content: txt("嗨")}, session.StopReason{})
	n = commitAll(t, db, "s1", c, n, nil)
	evs, wm, err := db.Replay(ctx, "s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[0].Kind != store.EventEntry || evs[0].Seq != 1 || evs[1].Seq != 2 || wm != 2 {
		t.Fatalf("回放 %+v wm=%d", evs, wm)
	}
	if d := evs[1].Data.(map[string]any); d["idx"] != 1 {
		t.Fatalf("idx = %v", d["idx"])
	}
	if evs, _, _ := db.Replay(ctx, "s1", 1); len(evs) != 1 || evs[0].Seq != 2 {
		t.Fatalf("after=1 应只剩一条: %+v", evs)
	}
	var got []bus.Envelope
	db.OnCommit(func(e bus.Envelope) { got = append(got, e) })
	_, _, _ = c.Input(txt("再来"), nil)
	commitAll(t, db, "s1", c, n, nil)
	if len(got) == 0 || got[0].Kind != store.EventEntry || got[0].Seq != 3 || got[0].Session != "s1" || got[0].V != bus.Version {
		t.Fatalf("提交通知 %+v", got)
	}
	evs, _, _ = db.Replay(ctx, "s1", 2)
	if len(evs) != len(got) || evs[0].Seq != got[0].Seq {
		t.Fatalf("通知与回放不一致: %+v vs %+v", got, evs)
	}
	db.OnCommit(nil)

	time.Sleep(2 * time.Millisecond)
	if err := db.CreateSession(ctx, store.SessionMeta{ID: "s1/sub-1", ParentID: "s1", Origin: "subagent"}, newCore().Snapshot().Header, nil); err != nil {
		t.Fatal(err)
	}
	all, err := db.ListSessions(ctx, 0, 10, false)
	if err != nil || len(all) != 2 || all[0].ID != "s1/sub-1" {
		t.Fatalf("列表（新的在前）%+v %v", all, err)
	}
	if page, _ := db.ListSessions(ctx, all[0].UpdatedAt, 10, false); len(page) != 1 || page[0].ID != "s1" {
		t.Fatalf("分页 %+v", page)
	}
	top, err := db.ListSessions(ctx, 0, 10, true)
	if err != nil || len(top) != 1 || top[0].ID != "s1" || top[0].Title != "一" || top[0].RunState != "running" {
		t.Fatalf("顶层列表 %+v %v", top, err)
	}
	if lim, _ := db.ListSessions(ctx, 0, 1, false); len(lim) != 1 {
		t.Fatalf("limit %+v", lim)
	}
	if _, _, err := db.Replay(ctx, "none", 0); err == nil {
		t.Fatal("不存在的会话应报错")
	}
}

// —— 定义 ——//

func testDefinitions(t *testing.T, db store.Store) {
	ctx := context.Background()
	if _, _, err := db.GetDefinition(ctx, "agent", "a", 0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("不存在应 ErrNotFound: %v", err)
	}
	v1, _ := db.PutDefinition(ctx, "agent", "a", "one")
	v2, _ := db.PutDefinition(ctx, "agent", "a", "two")
	if v1 != 1 || v2 != 2 {
		t.Fatalf("版本号: %d %d", v1, v2)
	}
	if v, _ := db.PutDefinition(ctx, "tool", "a", "x"); v != 1 {
		t.Fatalf("不同 kind 各自计版本: %d", v)
	}
	if body, v, _ := db.GetDefinition(ctx, "agent", "a", 0); body != "two" || v != 2 {
		t.Fatalf("最新: %s %d", body, v)
	}
	if body, v, _ := db.GetDefinition(ctx, "agent", "a", 1); body != "one" || v != 1 {
		t.Fatalf("旧版本应保留: %s", body)
	}
	if _, _, err := db.GetDefinition(ctx, "agent", "a", 3); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("越界版本: %v", err)
	}
}

// —— inbox ——//

func testInbox(t *testing.T, db store.Store) {
	ctx := context.Background()
	c := newCore()
	create(t, db, store.SessionMeta{ID: "s1"}, c)
	ok, err := db.Admit(ctx, "s1", store.InboxItem{ItemID: "i1", Kind: store.InboxInput, Input: `[]`})
	if err != nil || !ok {
		t.Fatalf("首次准入: %v %v", ok, err)
	}
	if ok, _ := db.Admit(ctx, "s1", store.InboxItem{ItemID: "i1", Kind: store.InboxInput, Input: `[]`}); ok {
		t.Fatal("待处理时重放应被拒")
	}
	_, _ = db.Admit(ctx, "s1", store.InboxItem{ItemID: "i2", Kind: store.InboxNote, Input: `[]`, Trust: "untrusted"})
	items, _ := db.Inbox(ctx, "s1")
	if len(items) != 2 || items[0].ItemID != "i1" || items[0].Trust != "trusted" || items[1].Trust != "untrusted" || items[0].At.IsZero() {
		t.Fatalf("顺序 / 信任: %+v", items)
	}
	if in, _ := db.InInbox(ctx, "s1", "i1"); !in {
		t.Fatal("i1 应待处理")
	}
	if err := db.Commit(ctx, store.Checkpoint{SessionID: "s1", Header: c.Snapshot().Header, ConsumeInbox: []string{"i1"}}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := db.Admit(ctx, "s1", store.InboxItem{ItemID: "i1", Kind: store.InboxInput, Input: `[]`}); ok {
		t.Fatal("已消费后重放仍应被拒（D6）")
	}
	if in, _ := db.InInbox(ctx, "s1", "i1"); in {
		t.Fatal("已消费不应待处理")
	}
	items, _ = db.Inbox(ctx, "s1")
	if len(items) != 1 || items[0].ItemID != "i2" {
		t.Fatalf("消费后: %+v", items)
	}
}

// —— 工具记录 ——//

func testToolStateMachine(t *testing.T, db store.Store) {
	ctx := context.Background()
	if err := db.PutToolStarted(ctx, "s1", "c1", "h1"); err != nil {
		t.Fatal(err)
	}
	if err := db.PutToolStarted(ctx, "s1", "c1", "h1"); !errors.Is(err, store.ErrStateMismatch) {
		t.Fatalf("重复 started 应拒绝: %v", err)
	}
	if err := db.FinishTool(ctx, "s1", "c1", store.ToolDelivered, "x"); !errors.Is(err, store.ErrStateMismatch) {
		t.Fatalf("错误的期望状态应拒绝: %v", err)
	}
	if err := db.FinishTool(ctx, "s1", "c1", store.ToolStarted, `"ok"`); err != nil {
		t.Fatal(err)
	}
	r, _ := db.GetTool(ctx, "s1", "c1")
	if r == nil || r.Status != store.ToolDone || r.Output != `"ok"` || r.ArgsHash != "h1" {
		t.Fatalf("记录不符: %+v", r)
	}
	if r, err := db.GetTool(ctx, "s1", "none"); r != nil || err != nil {
		t.Fatalf("缺失应 (nil, nil): %+v %v", r, err)
	}

	dl := time.Now().Add(time.Hour)
	if err := db.PutToolPending(ctx, store.ToolRecord{SessionID: "s1", CallID: "c2", Kind: "confirm", ArgsHash: "h2", Deadline: dl, TokenHash: "tk"}); err != nil {
		t.Fatal(err)
	}
	if err := db.PutToolStarted(ctx, "s2", "c2", ""); err != nil {
		t.Fatalf("另一会话同 call_id 应允许: %v", err)
	}
	ps, _ := db.PendingTools(ctx, "s1")
	if len(ps) != 1 || ps[0].CallID != "c2" || ps[0].Kind != "confirm" || ps[0].ArgsHash != "h2" ||
		ps[0].Deadline.UnixMilli() != dl.UnixMilli() || ps[0].SessionID != "s1" || ps[0].Status != store.ToolPending {
		t.Fatalf("挂起列表不符: %+v", ps)
	}
	if r, _ := db.GetTool(ctx, "s1", "c2"); r == nil || r.TokenHash != "tk" {
		t.Fatalf("token_hash: %+v", r)
	}
	if err := db.Deliver(ctx, "s1", "c2", "res", "approve"); err != nil {
		t.Fatal(err)
	}
	if err := db.Deliver(ctx, "s1", "c2", "", "approve"); !errors.Is(err, store.ErrStateMismatch) {
		t.Fatalf("重复送达应拒绝: %v", err)
	}
	if r, _ := db.GetTool(ctx, "s1", "c2"); r.Status != store.ToolDelivered || r.Delivered != "res" || r.Decision != "approve" {
		t.Fatalf("送达后: %+v", r)
	}
	if err := db.FinishTool(ctx, "s1", "c2", store.ToolDelivered, `"done"`); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishTool(ctx, "s1", "c3", "", `"r"`); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishTool(ctx, "s1", "c3", "", `"r"`); !errors.Is(err, store.ErrStateMismatch) {
		t.Fatalf("重复直接 done 应拒绝: %v", err)
	}
	_ = db.PutToolPending(ctx, store.ToolRecord{SessionID: "s1", CallID: "c4", Kind: "result"})
	_ = db.PutToolStarted(ctx, "s1", "c5", "")
	if err := db.DeleteFinishedTools(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"c1", "c2", "c3", "c5"} {
		if r, _ := db.GetTool(ctx, "s1", id); r != nil {
			t.Fatalf("%s 应被清理: %+v", id, r)
		}
	}
	if r, _ := db.GetTool(ctx, "s1", "c4"); r == nil || r.Status != store.ToolPending {
		t.Fatalf("pending 不应被清理")
	}
	if r, _ := db.GetTool(ctx, "s2", "c2"); r == nil {
		t.Fatal("别的会话不应被清理")
	}
}

func testConfirmLifecycle(t *testing.T, db store.Store) {
	ctx := context.Background()
	if err := db.PendConfirm(ctx, "s", "c", "h1", "写操作", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.PendConfirm(ctx, "s", "c", "h1", "写操作", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("重复挂起应幂等: %v", err)
	}
	if r, ok, _ := db.LookupTool(ctx, "s", "c"); !ok || r.Status != store.ToolPending {
		t.Fatalf("挂起: %+v", r)
	}
	if err := db.StartTool(ctx, "s", "c", "h1"); !errors.Is(err, store.ErrStateMismatch) {
		t.Fatalf("未批准不能 started: %v", err)
	}
	if err := db.Decide(ctx, "s", "c", true); err != nil {
		t.Fatal(err)
	}
	if err := db.StartTool(ctx, "s", "c", "h2"); !errors.Is(err, store.ErrStateMismatch) {
		t.Fatalf("参数哈希不同不能 started: %v", err)
	}
	if err := db.StartTool(ctx, "s", "c", "h1"); err != nil {
		t.Fatalf("批准后应可 started: %v", err)
	}
	if err := db.PendConfirm(ctx, "s", "c", "h9", "写操作", time.Now()); err != nil {
		t.Fatalf("非 pending 时 PendConfirm 也不报错: %v", err)
	}
	if err := db.FinishTool(ctx, "s", "c", store.ToolStarted, `{"text":"ok"}`); err != nil {
		t.Fatal(err)
	}
	r, ok, err := db.LookupTool(ctx, "s", "c")
	if err != nil || !ok || r.Status != store.ToolDone || r.Decision != "approve" || r.ArgsHash != "h1" {
		t.Fatalf("终态: %+v %v %v", r, ok, err)
	}
	if _, ok, err := db.LookupTool(ctx, "s", "none"); ok || err != nil {
		t.Fatalf("缺失: %v %v", ok, err)
	}
	if err := db.StartTool(ctx, "s", "fresh", "h"); err != nil {
		t.Fatalf("无记录应插入 started: %v", err)
	}
	_ = db.PendConfirm(ctx, "s", "d", "h", "", time.Now().Add(time.Hour))
	if err := db.Decide(ctx, "s", "d", false); err != nil {
		t.Fatal(err)
	}
	if r, _, _ := db.LookupTool(ctx, "s", "d"); r.Decision != "deny" || r.Status != store.ToolDelivered {
		t.Fatalf("拒绝: %+v", r)
	}
}

func testPendConfirmRefresh(t *testing.T, db store.Store) {
	ctx := context.Background()
	if err := db.PendConfirm(ctx, "s", "c", "old", "r", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	dl := time.Now().Add(2 * time.Hour)
	if err := db.PendConfirm(ctx, "s", "c", "new", "r", dl); err != nil {
		t.Fatal(err)
	}
	if ps, _ := db.PendingTools(ctx, "s"); len(ps) != 1 || ps[0].Deadline.UnixMilli() != dl.UnixMilli() {
		t.Fatalf("截止应刷新: %+v", ps)
	}
	if err := db.Decide(ctx, "s", "c", true); err != nil {
		t.Fatal(err)
	}
	if err := db.StartTool(ctx, "s", "c", "new"); err != nil {
		t.Fatal(err)
	}
}

// —— 删除 ——//

func testDelete(t *testing.T, db store.Store) {
	ctx := context.Background()
	c := newCore()
	create(t, db, store.SessionMeta{ID: "s1"}, c)
	_, _, _ = c.Input(txt("hi"), nil)
	commitAll(t, db, "s1", c, 0, nil)
	_, _ = db.Admit(ctx, "s1", store.InboxItem{ItemID: "i1", Kind: store.InboxInput, Input: `[]`})
	_ = db.PutToolStarted(ctx, "s1", "c1", "")
	boom := errors.New("boom")
	if err := db.DeleteSession(ctx, "s1", []store.Cleaner{func(context.Context, string) error { return boom }}); !errors.Is(err, boom) {
		t.Fatalf("回调失败应返回错误: %v", err)
	}
	if _, err := db.Load(ctx, "s1", nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("墓碑会话应不可见: %v", err)
	}
	if ok, _ := db.Exists(ctx, "s1"); ok {
		t.Fatal("墓碑会话 Exists 应为 false")
	}
	if ls, _ := db.ListSessions(ctx, 0, 10, false); len(ls) != 0 {
		t.Fatalf("墓碑会话不应列出: %+v", ls)
	}
	var called []string
	if err := db.DeleteSession(ctx, "s1", []store.Cleaner{func(_ context.Context, id string) error { called = append(called, id); return nil }}); err != nil {
		t.Fatalf("重试删除: %v", err)
	}
	if len(called) != 1 || called[0] != "s1" {
		t.Fatalf("回调未调用: %v", called)
	}
	if err := db.DeleteSession(ctx, "s1", nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("已删除再删应 ErrNotFound: %v", err)
	}
	if r, _ := db.GetTool(ctx, "s1", "c1"); r != nil {
		t.Fatal("工具记录应清理")
	}
	if items, _ := db.Inbox(ctx, "s1"); len(items) != 0 {
		t.Fatal("inbox 应清理")
	}
	// 删干净后 id 可复用，旧数据不残留
	create(t, db, store.SessionMeta{ID: "s1"}, newCore())
	if es, _ := db.Entries(ctx, "s1", 0, -1); len(es) != 0 {
		t.Fatalf("旧条目残留: %d", len(es))
	}
	if ok, _ := db.Admit(ctx, "s1", store.InboxItem{ItemID: "i1", Kind: store.InboxInput, Input: `[]`}); !ok {
		t.Fatal("旧 inbox 残留")
	}
}

// —— 会话元数据（schema v3）——//

func testMetaRoundTrip(t *testing.T, db store.Store) {
	ctx := context.Background()
	m := store.SessionMeta{ID: "s1", AgentID: "a", AgentHash: "abc", Creator: "local", SessionKey: "k1", Title: "t",
		ParentID: "p", Origin: "fork", ForkUpto: 3,
		Meta: json.RawMessage(`{"order":"42"}`), UseConfig: json.RawMessage(`{"permission":{"mode":"ask"}}`)}
	if err := db.CreateSession(ctx, m, newCore().Snapshot().Header, nil); err != nil {
		t.Fatal(err)
	}
	l, err := db.Load(ctx, "s1", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := l.Meta
	if got.ID != "s1" || got.AgentID != "a" || got.AgentHash != "abc" || got.Creator != "local" || got.SessionKey != "k1" ||
		got.Title != "t" || got.ParentID != "p" || got.Origin != "fork" || got.ForkUpto != 3 ||
		string(got.Meta) != `{"order":"42"}` || string(got.UseConfig) != `{"permission":{"mode":"ask"}}` {
		t.Fatalf("Meta 往返: %+v", got)
	}
	if err := db.CreateSession(ctx, store.SessionMeta{ID: "s2"}, newCore().Snapshot().Header, nil); err != nil {
		t.Fatal(err)
	}
	l2, _ := db.Load(ctx, "s2", nil)
	if l2.Meta.SessionKey != "" || string(l2.Meta.Meta) != "{}" || string(l2.Meta.UseConfig) != "{}" {
		t.Fatalf("缺省值: %+v", l2.Meta)
	}
}

func testSessionKeyUnique(t *testing.T, db store.Store) {
	ctx := context.Background()
	h := newCore().Snapshot().Header
	mk := func(id, agent, creator, key string) error {
		return db.CreateSession(ctx, store.SessionMeta{ID: id, AgentID: agent, Creator: creator, SessionKey: key}, h, nil)
	}
	if err := mk("s1", "a", "u", "k"); err != nil {
		t.Fatal(err)
	}
	if err := mk("s2", "a", "u", "k"); err == nil {
		t.Fatal("同（定义, 创建者, key）应冲突")
	}
	for i, c := range [][3]string{{"b", "u", "k"}, {"a", "v", "k"}, {"a", "u", "k2"}, {"a", "u", ""}, {"a", "u", ""}} {
		if err := mk("ok"+string(rune('0'+i)), c[0], c[1], c[2]); err != nil {
			t.Fatalf("%v 不应冲突: %v", c, err)
		}
	}
}

// —— 统计 ——//

func testStats(t *testing.T, db store.Store) {
	ctx := context.Background()
	c := newCore()
	create(t, db, store.SessionMeta{ID: "s"}, c)
	now := time.Now()
	spans := []store.Span{
		{ID: "a1", Kind: "attempt", Status: "ok", Attrs: json.RawMessage(`{"input":1000,"output":10,"cache_read":800,"usage_source":"Reported"}`)},
		{ID: "a2", Kind: "attempt", Status: "ok", Attrs: json.RawMessage(`{"input":1000,"output":20,"cache_read":0,"cache_write":5,"usage_source":"Reported"}`)},
		{ID: "a3", Kind: "attempt", Status: "error", Attrs: json.RawMessage(`{"error":"x"}`)},
		{ID: "t1", Kind: "tool", Status: "ok"},
		{ID: "c1", Kind: "compact", Status: "ok"},
		{ID: "st", Kind: "step", Status: "ok"},
	}
	for i := range spans {
		spans[i].Start = now.Add(time.Duration(i) * time.Millisecond)
	}
	if err := db.Commit(ctx, store.Checkpoint{SessionID: "s", Header: c.Snapshot().Header, Spans: spans,
		Request: &store.RequestRecord{RequestID: "r0", ShellHash: "h", FinalHash: "f", Composition: json.RawMessage(`{"total":1}`)}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	// span 按 id 覆盖更新：a3 再提交一次不重复计数
	a3 := spans[2]
	a3.End = now.Add(time.Second)
	if err := db.Commit(ctx, store.Checkpoint{SessionID: "s", Header: c.Snapshot().Header, Spans: []store.Span{a3},
		Request: &store.RequestRecord{RequestID: "r1", ShellHash: "h", FinalHash: "f", Composition: json.RawMessage(`{"total":5}`)}}); err != nil {
		t.Fatal(err)
	}
	st, err := db.SessionStats(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	if st.Attempts != 3 || st.Reported != 2 || st.Missing != 1 || st.Errors != 1 || st.ToolCalls != 1 || st.Compacts != 1 {
		t.Fatalf("counts %+v", st)
	}
	if st.Input != 2000 || st.Output != 30 || st.CacheRead != 800 || st.CacheWrite != 5 {
		t.Fatalf("usage %+v", st)
	}
	if r := st.HitRate(); r != 0.4 {
		t.Fatalf("hit rate %v", r)
	}
	if st.LastRequest != "r1" || string(st.Composition) != `{"total":5}` {
		t.Fatalf("last %+v", st)
	}
	empty, err := db.SessionStats(ctx, "none")
	if err != nil || empty.HitRate() != -1 {
		t.Fatalf("empty %+v %v", empty, err)
	}
}

// —— 导出 ——//

func exportKinds(t *testing.T, buf *bytes.Buffer) map[string]int {
	t.Helper()
	kinds := map[string]int{}
	sc := bufio.NewScanner(buf)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var rec struct {
			Kind string          `json:"kind"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatalf("非法 JSONL: %v", err)
		}
		kinds[rec.Kind]++
	}
	return kinds
}

func testExport(t *testing.T, db store.Store) {
	ctx := context.Background()
	c := newCore()
	create(t, db, store.SessionMeta{ID: "s1"}, c)
	_, n1, _ := c.Input(txt("你好"), nil)
	n := commitAll(t, db, "s1", c, 0, func(cp *store.Checkpoint) {
		cp.Request = &store.RequestRecord{RequestID: n1.Model.RequestID, ShellHash: "x", PrefixEnd: 1, FinalHash: "f"}
		cp.Spans = []store.Span{{ID: "sp1", Kind: "step", Start: time.Now()}}
	})
	_, _, _ = c.ModelDone(n1.Model.RequestID, n1.ID, session.AssistantItem{Content: txt("嗨")}, session.StopReason{})
	commitAll(t, db, "s1", c, n, nil)
	_ = db.FinishTool(ctx, "s1", "c1", "", `"r"`)
	var buf bytes.Buffer
	if err := db.Export(ctx, "s1", &buf); err != nil {
		t.Fatalf("Export: %v", err)
	}
	k := exportKinds(t, &buf)
	if k["session"] != 1 || k["entry"] != 2 || k["request"] != 1 || k["span"] != 1 || k["tool"] != 1 {
		t.Fatalf("导出记录 %v", k)
	}
	if err := db.Export(ctx, "none", &bytes.Buffer{}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("缺失会话导出: %v", err)
	}
}
