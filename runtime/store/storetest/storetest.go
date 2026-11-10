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
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/runtime/bus"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
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
		{"LeaseFencing", testLeaseFencing},
		{"SleepStatus", testSleepStatus},
		{"SleepingWithMail", testSleepingWithMail},
		{"PendingCorr", testPendingCorr},
		{"FindSessionByKey", testFindSessionByKey},
		{"Outbox", testOutbox},
		{"OutboxClaim", testOutboxClaim},
		{"ErrorKinds", testErrorKinds},
		{"TraceOrder", testTraceOrder},
		{"Prune", testPrune},
		{"TraceEvents", testTraceEvents},
		{"Tx", testSubStoresAndTx},
		{"TxRollback", testTxRollback},
		{"Concurrency", testConcurrency},
		{"CheckpointEvents", testCheckpointEvents},
		{"EventsCursorExpired", testEventsCursorExpired},
		{"Interactions", testInteractions},
		{"SessionMetaUpdate", testSessionMetaUpdate},
		{"Workspace", testWorkspace},
		{"CollectionRoundTrip", testCollectionRoundTrip},
		{"CollectionConflict", testCollectionConflict},
		{"CollectionQueryPage", testCollectionQueryPage},
		{"CollectionPartition", testCollectionPartition},
		{"CollectionTx", testCollectionTx},
		{"CollectionSpecStable", testCollectionSpecStable},
		{"EventLogRoundTrip", testEventLogRoundTrip},
		{"EventLogCursor", testEventLogCursor},
		{"EventLogTx", testEventLogTx},
		{"EventLogLimit", testEventLogLimit},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.f(t, open(t)) })
	}
}

// testWorkspace 会话的工作区：建会话时记下、子会话跟随父会话、Load 读回、ListSessionsIn 只列该工作区的（空 = 全部）。
func testWorkspace(t *testing.T, db store.Store) {
	ctx := context.Background()
	create(t, db, store.SessionMeta{ID: "a1", Workspace: "wa"}, newCore())
	create(t, db, store.SessionMeta{ID: "a1/sub", ParentID: "a1", Origin: "subagent"}, newCore())
	create(t, db, store.SessionMeta{ID: "b1", Workspace: "wb"}, newCore())
	create(t, db, store.SessionMeta{ID: "none"}, newCore())
	l, err := db.Load(ctx, "a1/sub", nil)
	if err != nil || l.Meta.Workspace != "wa" {
		t.Fatalf("子会话应跟随父会话的工作区：%+v %v", l, err)
	}
	ids := func(ws string, top bool) map[string]string {
		ls, err := db.ListSessionsIn(ctx, ws, 0, 50, top)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]string{}
		for _, x := range ls {
			m[x.ID] = x.Workspace
		}
		return m
	}
	if m := ids("wa", false); len(m) != 2 || m["a1"] != "wa" || m["a1/sub"] != "wa" {
		t.Fatalf("wa：%v", m)
	}
	if m := ids("wa", true); len(m) != 1 {
		t.Fatalf("wa 顶层：%v", m)
	}
	if m := ids("wb", true); len(m) != 1 || m["b1"] != "wb" {
		t.Fatalf("wb：%v", m)
	}
	if m := ids("", true); len(m) != 3 {
		t.Fatalf("不过滤：%v", m)
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
	n1, _ := c.Input(txt("你好"), nil)
	n := commitAll(t, db, "s1", c, 0, func(cp *store.Checkpoint) {
		cp.Request = &store.RequestRecord{RequestID: n1.Model.RequestID, ShellHash: "x", ShellBody: []byte(`{"a":1}`),
			PrefixEnd: 1, FinalHash: "f", Composition: json.RawMessage(`{"system":3}`)}
		cp.Spans = []store.Span{{ID: "sp1", Kind: "step", Start: time.Now()}}
		cp.Preview = "你好"
	})
	_, _ = c.ModelDone(n1.Model.RequestID, n1.ID, session.AssistantItem{Content: txt("嗨")}, session.StopReason{})
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
	if _, err := db.ShellBody(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("缺失外壳应报 store.ErrNotFound：%v", err)
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
	if err := c.Rewind(1); err != nil {
		t.Fatalf("Rewind: %v", err)
	}
	c.Record(txt("c"), nil)
	from := 1
	s := c.Snapshot()
	if err := db.Commit(ctx, store.Checkpoint{SessionID: "s", ExpectLogLen: n, TruncateFrom: &from,
		AppendFrom: 1, Append: s.Entries[1:], Header: s.Header}); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	// a=1, b=2，截断占 3，c=4。事件表只追加（06 10.2.1）：被截掉的 b 的追加事件仍在，后面跟着回退，
	// 按顺序重放得到同样的日志；b 的正文从归档取回。
	evs, wm, err := db.Replay(ctx, "s", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 4 || wm != 4 {
		t.Fatalf("回放 %d 条 wm=%d", len(evs), wm)
	}
	for i, k := range []string{store.EventEntry, store.EventEntry, store.EventRewind, store.EventEntry} {
		if evs[i].Seq != int64(i+1) || evs[i].Kind != k {
			t.Fatalf("第 %d 条：seq=%d kind=%s，期望 %d %s", i, evs[i].Seq, evs[i].Kind, i+1, k)
		}
	}
	if to := evs[2].Data.(map[string]any)["to"]; to != 1 {
		t.Fatalf("rewind to = %v", to)
	}
	if b, _ := evs[1].Data.(map[string]any)["entry"].(json.RawMessage); !bytes.Contains(b, []byte(`"b"`)) {
		t.Fatalf("被截掉的条目正文应从归档取回：%s", b)
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
	n1, _ := c.Input(txt("你好"), nil)
	n := commitAll(t, db, "s1", c, 0, nil)
	_, _ = c.ModelDone(n1.Model.RequestID, n1.ID, session.AssistantItem{Content: txt("嗨")}, session.StopReason{})
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
	_, _ = c.Input(txt("再来"), nil)
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

func testDefinitions(t *testing.T, st store.Store) {
	ctx := context.Background()
	db, ok := st.(store.DefinitionStore)
	if !ok {
		t.Skip("存储未实现可选的 DefinitionStore")
	}
	if _, _, err := db.GetDefinition(ctx, "agent", "a", 0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("不存在应 ErrNotFound: %v", err)
	}
	v1, h1, _ := db.PutDefinition(ctx, "agent", "a", "one")
	v2, h2, _ := db.PutDefinition(ctx, "agent", "a", "two")
	if v1 != 1 || v2 != 2 {
		t.Fatalf("版本号: %d %d", v1, v2)
	}
	if h1 == "" || h1 == h2 || h2 != store.HashBytes([]byte("two")) {
		t.Fatalf("哈希: %q %q", h1, h2)
	}
	if v, _, _ := db.PutDefinition(ctx, "tool", "a", "x"); v != 1 {
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
	if ids, err := db.ListDefinitions(ctx, "agent"); err != nil || len(ids) != 1 || ids[0] != "a" {
		t.Fatalf("列定义: %v %v", ids, err)
	}
	if _, _, err := db.PutDefinition(ctx, "agent", "b", "three"); err != nil {
		t.Fatalf("写第二个定义: %v", err)
	}
	if ids, _ := db.ListDefinitions(ctx, "agent"); len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("列定义按 id 升序: %v", ids)
	}
	if ids, err := db.ListDefinitions(ctx, "nope"); err != nil || len(ids) != 0 {
		t.Fatalf("空定义类: %v %v", ids, err)
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
	_, _ = db.Admit(ctx, "s1", store.InboxItem{ItemID: "i2", Kind: store.InboxNote, Input: `[]`, Trust: "untrusted",
		Envelope: `{"id":"i2","from":{"kind":"human","id":"u1"}}`})
	items, _ := db.Inbox(ctx, "s1")
	if len(items) != 2 || items[0].ItemID != "i1" || items[0].Trust != "trusted" || items[1].Trust != "untrusted" || items[0].At.IsZero() {
		t.Fatalf("顺序 / 信任: %+v", items)
	}
	if items[1].Envelope == "" || items[0].Envelope != "" {
		t.Fatalf("信封列往返: %+v", items)
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
	if err := db.PendConfirm(ctx, "s", "c", "h1", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.PendConfirm(ctx, "s", "c", "h1", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("重复挂起应幂等: %v", err)
	}
	if r, ok, _ := lookup(ctx, db, "s", "c"); !ok || r.Status != store.ToolPending {
		t.Fatalf("挂起: %+v", r)
	}
	if err := db.StartTool(ctx, "s", "c", "h1"); !errors.Is(err, store.ErrStateMismatch) {
		t.Fatalf("未批准不能 started: %v", err)
	}
	if err := db.Deliver(ctx, "s", "c", "", "approve"); err != nil {
		t.Fatal(err)
	}
	if err := db.StartTool(ctx, "s", "c", "h2"); !errors.Is(err, store.ErrStateMismatch) {
		t.Fatalf("参数哈希不同不能 started: %v", err)
	}
	if err := db.StartTool(ctx, "s", "c", "h1"); err != nil {
		t.Fatalf("批准后应可 started: %v", err)
	}
	if err := db.PendConfirm(ctx, "s", "c", "h9", time.Now()); err != nil {
		t.Fatalf("非 pending 时 PendConfirm 也不报错: %v", err)
	}
	if err := db.FinishTool(ctx, "s", "c", store.ToolStarted, `{"text":"ok"}`); err != nil {
		t.Fatal(err)
	}
	r, ok, err := lookup(ctx, db, "s", "c")
	if err != nil || !ok || r.Status != store.ToolDone || r.Decision != "approve" || r.ArgsHash != "h1" {
		t.Fatalf("终态: %+v %v %v", r, ok, err)
	}
	if _, ok, err := lookup(ctx, db, "s", "none"); ok || err != nil {
		t.Fatalf("缺失: %v %v", ok, err)
	}
	if err := db.StartTool(ctx, "s", "fresh", "h"); err != nil {
		t.Fatalf("无记录应插入 started: %v", err)
	}
	_ = db.PendConfirm(ctx, "s", "d", "h", time.Now().Add(time.Hour))
	if err := db.Deliver(ctx, "s", "d", "", "deny"); err != nil {
		t.Fatal(err)
	}
	if r, _, _ := lookup(ctx, db, "s", "d"); r.Decision != "deny" || r.Status != store.ToolDelivered {
		t.Fatalf("拒绝: %+v", r)
	}
}

func testPendConfirmRefresh(t *testing.T, db store.Store) {
	ctx := context.Background()
	if err := db.PendConfirm(ctx, "s", "c", "old", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	dl := time.Now().Add(2 * time.Hour)
	if err := db.PendConfirm(ctx, "s", "c", "new", dl); err != nil {
		t.Fatal(err)
	}
	if ps, _ := db.PendingTools(ctx, "s"); len(ps) != 1 || ps[0].Deadline.UnixMilli() != dl.UnixMilli() {
		t.Fatalf("截止应刷新: %+v", ps)
	}
	if err := db.Deliver(ctx, "s", "c", "", "approve"); err != nil {
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
	_, _ = c.Input(txt("hi"), nil)
	commitAll(t, db, "s1", c, 0, nil)
	_, _ = db.Admit(ctx, "s1", store.InboxItem{ItemID: "i1", Kind: store.InboxInput, Input: `[]`})
	_ = db.PutToolStarted(ctx, "s1", "c1", "")
	_ = db.IssueCorr(ctx, store.Pending{Corr: "del-corr", SessionID: "s1"})
	_ = db.RecordTrace(ctx, store.TraceEvent{Trace: "del-trace", Session: "s1", Kind: "envelope"})
	o := c.Snapshot()
	if err := db.Commit(ctx, store.Checkpoint{SessionID: "s1", ExpectLogLen: o.Header.LogLen, Header: o.Header,
		Outbox: []store.OutboxItem{{ID: "del-out", Envelope: []byte(`{}`)}}}); err != nil {
		t.Fatal(err)
	}
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
	// 待回复、发件箱、trace 事件也一并清掉（D 组：sqlite 原来漏删这三张表）。
	if _, err := db.PendingCorr(ctx, "del-corr"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("待回复应清理: %v", err)
	}
	if items, _ := db.Outbox(ctx, 0); len(items) != 0 {
		t.Fatalf("发件箱应清理: %+v", items)
	}
	if evs, _ := db.TraceEvents(ctx, "del-trace"); len(evs) != 0 {
		t.Fatalf("trace 事件应清理: %+v", evs)
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
	n1, _ := c.Input(txt("你好"), nil)
	n := commitAll(t, db, "s1", c, 0, func(cp *store.Checkpoint) {
		cp.Request = &store.RequestRecord{RequestID: n1.Model.RequestID, ShellHash: "x", PrefixEnd: 1, FinalHash: "f"}
		cp.Spans = []store.Span{{ID: "sp1", Kind: "step", Start: time.Now()}}
	})
	_, _ = c.ModelDone(n1.Model.RequestID, n1.ID, session.AssistantItem{Content: txt("嗨")}, session.StopReason{})
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

// testLeaseFencing 验证 02 2.2 的租约带纪元：抢租约纪元递增、旧纪元写入被拒、旧持有者续租失败。
func testLeaseFencing(t *testing.T, db store.Store) {
	ctx := context.Background()
	id := "s1"
	create(t, db, store.SessionMeta{ID: id}, newCore())

	if _, err := db.AcquireLease(ctx, "none", "node-a"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("缺失会话抢租约: %v", err)
	}
	epoch1, err := db.AcquireLease(ctx, id, "node-a")
	if err != nil || epoch1 != 1 {
		t.Fatalf("首次抢租约: epoch=%d err=%v", epoch1, err)
	}
	if err := db.RenewLease(ctx, id, "node-a", epoch1); err != nil {
		t.Fatalf("续租: %v", err)
	}
	hdr := newCore().Snapshot().Header
	if err := db.Commit(ctx, store.Checkpoint{SessionID: id, ExpectLogLen: 0, Epoch: epoch1, Header: hdr}); err != nil {
		t.Fatalf("持租约提交: %v", err)
	}
	// 另一个节点接管：纪元加一。
	epoch2, err := db.AcquireLease(ctx, id, "node-b")
	if err != nil || epoch2 != epoch1+1 {
		t.Fatalf("接管: epoch=%d err=%v", epoch2, err)
	}
	// 旧节点的迟到写入被存储拒绝。
	err = db.Commit(ctx, store.Checkpoint{SessionID: id, ExpectLogLen: 0, Epoch: epoch1, Header: hdr})
	if !errors.Is(err, store.ErrLeaseLost) {
		t.Fatalf("旧纪元写入应被拒: %v", err)
	}
	if err := db.RenewLease(ctx, id, "node-a", epoch1); !errors.Is(err, store.ErrLeaseLost) {
		t.Fatalf("旧持有者续租应失败: %v", err)
	}
	// 新纪元写入成功（接管者推进日志）；释放后不能再用。
	c2 := newCore()
	_, _ = c2.Input(txt("接管者写入"), nil)
	s2 := c2.Snapshot()
	if err := db.Commit(ctx, store.Checkpoint{SessionID: id, ExpectLogLen: 0, Epoch: epoch2, Header: s2.Header,
		Append: s2.Entries, AppendFrom: 0}); err != nil {
		t.Fatalf("新纪元提交: %v", err)
	}
	// 接管者推进日志后，旧节点的迟到写入纪元和 log_len 都不符：应报租约失效，不是普通冲突。
	err = db.Commit(ctx, store.Checkpoint{SessionID: id, ExpectLogLen: 0, Epoch: epoch1, Header: hdr})
	if !errors.Is(err, store.ErrLeaseLost) {
		t.Fatalf("旧纪元 + 过期 log_len 应报租约失效: %v", err)
	}
	if err := db.ReleaseLease(ctx, id, "node-b", epoch2); err != nil {
		t.Fatalf("释放: %v", err)
	}
	if err := db.RenewLease(ctx, id, "node-b", epoch2); !errors.Is(err, store.ErrLeaseLost) {
		t.Fatalf("释放后续租应失败: %v", err)
	}
}

func testSleepStatus(t *testing.T, db store.Store) {
	ctx := context.Background()
	id := "s1"
	create(t, db, store.SessionMeta{ID: id}, newCore())

	if st, err := db.SessionStatus(ctx, id); err != nil || st != store.StatusActive {
		t.Fatalf("新建会话状态: %q %v", st, err)
	}
	if _, err := db.SessionStatus(ctx, "none"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("缺失会话状态: %v", err)
	}
	if err := db.SetSessionStatus(ctx, id, store.StatusSleeping, store.StatusActive, 0); !errors.Is(err, store.ErrStateMismatch) {
		t.Fatalf("active 会话不能按 sleeping→active 迁移: %v", err)
	}
	epoch, err := db.AcquireLease(ctx, id, "node-a")
	if err != nil {
		t.Fatalf("抢租约: %v", err)
	}
	// 休眠：持租约时 active→sleeping，带纪元。
	if err := db.SetSessionStatus(ctx, id, store.StatusActive, store.StatusSleeping, epoch); err != nil {
		t.Fatalf("休眠: %v", err)
	}
	if st, _ := db.SessionStatus(ctx, id); st != store.StatusSleeping {
		t.Fatalf("休眠后状态: %q", st)
	}
	if ids, err := db.SessionsWithStatus(ctx, store.StatusSleeping, 10); err != nil || len(ids) != 1 || ids[0] != id {
		t.Fatalf("列休眠会话: %v %v", ids, err)
	}
	// 换节点接管后纪元变新：旧纪元不能唤醒，新纪元可以。
	epoch2, err := db.AcquireLease(ctx, id, "node-b")
	if err != nil || epoch2 == epoch {
		t.Fatalf("接管: epoch=%d err=%v", epoch2, err)
	}
	if err := db.SetSessionStatus(ctx, id, store.StatusSleeping, store.StatusActive, epoch); !errors.Is(err, store.ErrStateMismatch) {
		t.Fatalf("旧纪元唤醒应失败: %v", err)
	}
	if err := db.SetSessionStatus(ctx, id, store.StatusSleeping, store.StatusActive, epoch2); err != nil {
		t.Fatalf("唤醒: %v", err)
	}
	if st, _ := db.SessionStatus(ctx, id); st != store.StatusActive {
		t.Fatalf("唤醒后状态: %q", st)
	}
	if ids, _ := db.SessionsWithStatus(ctx, store.StatusSleeping, 0); len(ids) != 0 {
		t.Fatalf("唤醒后不应再有休眠会话: %v", ids)
	}
}

// —— 待回复表（pending corr） ——//

func testPendingCorr(t *testing.T, db store.Store) {
	ctx := context.Background()
	c := newCore()
	create(t, db, store.SessionMeta{ID: "s1"}, c)
	p := store.Pending{Corr: "corr-1", SessionID: "s1", ReplyTo: "agent:worker", IssuedIdx: 5, Trace: "t1"}
	if err := db.IssueCorr(ctx, p); err != nil {
		t.Fatal(err)
	}
	if got, err := db.PendingCorr(ctx, "corr-1"); err != nil || got.SessionID != "s1" || got.Used {
		t.Fatalf("读登记: %+v %v", got, err)
	}
	// 发送者不是预期回复方 → 拒。
	if _, err := db.ClaimCorr(ctx, "corr-1", "s1", "agent:other"); !errors.Is(err, store.ErrCorrRejected) {
		t.Fatalf("预期回复方不符应拒: %v", err)
	}
	// 正确回信 → 占用；只收第一条。
	if _, err := db.ClaimCorr(ctx, "corr-1", "s1", "agent:worker"); err != nil {
		t.Fatalf("首条回信: %v", err)
	}
	if _, err := db.ClaimCorr(ctx, "corr-1", "s1", "agent:worker"); !errors.Is(err, store.ErrCorrRejected) {
		t.Fatal("重放的回信应被拒")
	}
	// Rewind 到更早位置：其后签发的未用 corr 作废。
	if err := db.IssueCorr(ctx, store.Pending{Corr: "corr-2", SessionID: "s1", IssuedIdx: 6}); err != nil {
		t.Fatal(err)
	}
	if err := db.IssueCorr(ctx, store.Pending{Corr: "corr-3", SessionID: "s1", IssuedIdx: 2}); err != nil {
		t.Fatal(err)
	}
	if n, err := db.VoidCorrAfter(ctx, "s1", 5); err != nil || n != 1 {
		t.Fatalf("作废数: %d %v", n, err)
	}
	if _, err := db.ClaimCorr(ctx, "corr-2", "s1", ""); !errors.Is(err, store.ErrCorrRejected) {
		t.Fatal("作废后应拒")
	}
	if _, err := db.ClaimCorr(ctx, "corr-3", "s1", ""); err != nil {
		t.Fatalf("有签发位置更早，应该仍可回信: %v", err)
	}
	// 过期：deadline 已过 → 拒。
	if err := db.IssueCorr(ctx, store.Pending{Corr: "corr-4", SessionID: "s1", Deadline: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ClaimCorr(ctx, "corr-4", "s1", ""); !errors.Is(err, store.ErrCorrRejected) {
		t.Fatal("过期回信应被拒")
	}
	// 单条作废：之后的回信被拒；对已作废 / 不存在的 corr 再作废不报错。
	if err := db.IssueCorr(ctx, store.Pending{Corr: "corr-5", SessionID: "s1"}); err != nil {
		t.Fatal(err)
	}
	if err := db.VoidCorr(ctx, "corr-5"); err != nil {
		t.Fatalf("VoidCorr: %v", err)
	}
	if _, err := db.ClaimCorr(ctx, "corr-5", "s1", ""); !errors.Is(err, store.ErrCorrRejected) {
		t.Fatal("作废后的回信应被拒")
	}
	if err := db.VoidCorr(ctx, "corr-5"); err != nil {
		t.Fatalf("重复作废不应报错: %v", err)
	}
	if err := db.VoidCorr(ctx, "no-such"); err != nil {
		t.Fatalf("作废不存在的 corr 不应报错: %v", err)
	}
}

// FindSessionByKey 按会话键查会话（13 13.3 B19 的原子查或建基础）。
func testFindSessionByKey(t *testing.T, db store.Store) {
	ctx := context.Background()
	h := newCore().Snapshot().Header
	mk := func(id, agent, creator, key string) {
		t.Helper()
		if err := db.CreateSession(ctx, store.SessionMeta{ID: id, AgentID: agent, Creator: creator, SessionKey: key}, h, nil); err != nil {
			t.Fatal(err)
		}
	}
	mk("k1", "a", "u", "key-1")
	mk("k2", "a", "u2", "key-1")
	if _, err := db.FindSessionByKey(ctx, "a", "u", "key-1"); err != nil {
		t.Fatalf("查已存在键: %v", err)
	}
	id, err := db.FindSessionByKey(ctx, "a", "u", "key-1")
	if err != nil || id != "k1" {
		t.Fatalf("id/err = %q/%v，期望 k1", id, err)
	}
	if _, err := db.FindSessionByKey(ctx, "a", "u", "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("不存在键 err = %v，期望 ErrNotFound", err)
	}
	if _, err := db.FindSessionByKey(ctx, "b", "u", "key-1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("换定义 err = %v，期望 ErrNotFound", err)
	}
}

// testOutbox 检查发件箱：与检查点同事务写入（Commit 的 Outbox），派发后标记不再返回。
func testOutbox(t *testing.T, db store.Store) {
	t.Helper()
	ctx := context.Background()
	h := newCore().Snapshot().Header
	if err := db.CreateSession(ctx, store.SessionMeta{ID: "o1"}, h, nil); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	now := int64(1000)
	cp := store.Checkpoint{SessionID: "o1", Header: h, RunState: "idle",
		Outbox: []store.OutboxItem{
			{ID: "m1", Envelope: []byte(`{"id":"m1"}`), CreatedAt: now},
			{ID: "m2", Envelope: []byte(`{"id":"m2"}`), CreatedAt: now + 1},
		}}
	if err := db.Commit(ctx, cp); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	items, err := db.Outbox(ctx, 0)
	if err != nil {
		t.Fatalf("Outbox: %v", err)
	}
	if len(items) != 2 || items[0].ID != "m1" || items[1].ID != "m2" {
		t.Fatalf("Outbox = %+v", items)
	}
	if items[0].SessionID != "o1" {
		t.Fatalf("session_id = %q", items[0].SessionID)
	}
	if err := db.MarkOutbox(ctx, "m1"); err != nil {
		t.Fatalf("MarkOutbox: %v", err)
	}
	items, _ = db.Outbox(ctx, 0)
	if len(items) != 1 || items[0].ID != "m2" {
		t.Fatalf("标记后 Outbox = %+v", items)
	}
	// 已派发的行保留：同 id 重提交不会再入队；标记未知 id 不报错。
	cp.ExpectLogLen, cp.Outbox = 0, []store.OutboxItem{{ID: "m1", Envelope: []byte(`{"id":"m1"}`), CreatedAt: now + 5}}
	if err := db.Commit(ctx, cp); err != nil {
		t.Fatalf("重提交: %v", err)
	}
	if items, _ = db.Outbox(ctx, 0); len(items) != 1 || items[0].ID != "m2" {
		t.Fatalf("已派发的 m1 不应重新入队：%+v", items)
	}
	if err := db.MarkOutbox(ctx, "no-such"); err != nil {
		t.Fatalf("标记未知 id 不应报错: %v", err)
	}
}

// testErrorKinds 两个实现对同一种失败给同一种错误（D 组）：调用方靠 errors.Is 分支，
// 实现之间不一致就意味着某条分支在另一种存储上永远走不到。
func testErrorKinds(t *testing.T, db store.Store) {
	ctx := context.Background()
	h := newCore().Snapshot().Header
	if err := db.CreateSession(ctx, store.SessionMeta{ID: "ek", AgentID: "a", Creator: "u", SessionKey: "k"}, h, nil); err != nil {
		t.Fatal(err)
	}
	// 重复 id / 重复会话键 → ErrConflict（host 并发按 key 建会话时据此重查）。
	if err := db.CreateSession(ctx, store.SessionMeta{ID: "ek"}, h, nil); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("重复 id 应 ErrConflict，得 %v", err)
	}
	if err := db.CreateSession(ctx, store.SessionMeta{ID: "ek2", AgentID: "a", Creator: "u", SessionKey: "k"}, h, nil); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("重复会话键应 ErrConflict，得 %v", err)
	}
	// 空会话键查不到（键为空的会话不参与查找）。
	if err := db.CreateSession(ctx, store.SessionMeta{ID: "nokey", AgentID: "a", Creator: "u"}, h, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FindSessionByKey(ctx, "a", "u", ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("空键应 ErrNotFound，得 %v", err)
	}
	// 未知会话：Replay / SetSessionStatus / SessionStatus 都是 ErrNotFound。
	if _, _, err := db.Replay(ctx, "no-such", 0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Replay 未知会话应 ErrNotFound，得 %v", err)
	}
	if err := db.SetSessionStatus(ctx, "no-such", store.StatusActive, store.StatusSleeping, 0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("SetSessionStatus 未知会话应 ErrNotFound，得 %v", err)
	}
	if _, err := db.SessionStatus(ctx, "no-such"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("SessionStatus 未知会话应 ErrNotFound，得 %v", err)
	}
	// 状态不符：ErrStateMismatch（不是 ErrNotFound）。
	if err := db.SetSessionStatus(ctx, "ek", store.StatusSleeping, store.StatusActive, 0); !errors.Is(err, store.ErrStateMismatch) {
		t.Fatalf("状态不符应 ErrStateMismatch，得 %v", err)
	}
	// 墓碑：清理回调失败、墓碑保留时，SessionStatus 报 deleted（host 据此拒投「已关闭」）。
	boom := errors.New("boom")
	_ = db.DeleteSession(ctx, "ek", []store.Cleaner{func(context.Context, string) error { return boom }})
	if st, err := db.SessionStatus(ctx, "ek"); err != nil || st != "deleted" {
		t.Fatalf("墓碑的 SessionStatus 应为 deleted，得 %q %v", st, err)
	}
}

// testTraceOrder trace 事件按发生时间返回，与记录顺序无关。
func testTraceOrder(t *testing.T, db store.Store) {
	ctx := context.Background()
	for _, ev := range []store.TraceEvent{{Trace: "o", Kind: "b", At: 200}, {Trace: "o", Kind: "a", At: 100}, {Trace: "o", Kind: "c", At: 300}} {
		if err := db.RecordTrace(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	evs, err := db.TraceEvents(ctx, "o")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range evs {
		got = append(got, e.Kind)
	}
	if strings.Join(got, "") != "abc" {
		t.Fatalf("trace 事件应按时间排序，得 %v", got)
	}
}

// testPrune 过期数据清理（D 组）：只删「早于截止时间且已经没用」的行——已用 / 已过期的待回复、
// 已派发或死信的外发、trace 事件；未用的待回复与未派发的外发不动。
func testPrune(t *testing.T, db store.Store) {
	ctx := context.Background()
	pr, ok := db.(store.PruneStore)
	if !ok {
		t.Skip("存储未实现可选的 PruneStore")
	}
	h := newCore().Snapshot().Header
	if err := db.CreateSession(ctx, store.SessionMeta{ID: "pr"}, h, nil); err != nil {
		t.Fatal(err)
	}
	_ = db.IssueCorr(ctx, store.Pending{Corr: "p-used", SessionID: "pr"})
	_ = db.IssueCorr(ctx, store.Pending{Corr: "p-open", SessionID: "pr"})
	_ = db.VoidCorr(ctx, "p-used")
	if err := db.Commit(ctx, store.Checkpoint{SessionID: "pr", Header: h, Outbox: []store.OutboxItem{
		{ID: "o-sent", Envelope: []byte(`{}`), CreatedAt: 1}, {ID: "o-dead", Envelope: []byte(`{}`), CreatedAt: 2},
		{ID: "o-open", Envelope: []byte(`{}`), CreatedAt: 3}}}); err != nil {
		t.Fatal(err)
	}
	_ = db.MarkOutbox(ctx, "o-sent")
	_ = db.DeadOutbox(ctx, "o-dead", "x")
	_ = db.RecordTrace(ctx, store.TraceEvent{Trace: "pt", Session: "pr", Kind: "k", At: 1})
	n, err := pr.Prune(ctx, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n < 4 {
		t.Fatalf("应清掉已用待回复、已派发与死信外发、trace 事件（≥4 行），得 %d", n)
	}
	if _, err := db.PendingCorr(ctx, "p-used"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("已用的待回复应清掉: %v", err)
	}
	if _, err := db.PendingCorr(ctx, "p-open"); err != nil {
		t.Fatalf("未用的待回复不应清: %v", err)
	}
	items, _ := db.Outbox(ctx, 0)
	if len(items) != 1 || items[0].ID != "o-open" {
		t.Fatalf("未派发的外发不应清：%+v", items)
	}
	if evs, _ := db.TraceEvents(ctx, "pt"); len(evs) != 0 {
		t.Fatalf("早于截止时间的 trace 事件应清掉: %+v", evs)
	}
	// 截止时间之前没有可清的：什么也不删。
	if n, err := pr.Prune(ctx, time.Unix(0, 0)); err != nil || n != 0 {
		t.Fatalf("截止时间之前不应清任何行，得 %d %v", n, err)
	}
}

// testOutboxClaim 检查发件箱认领（C6）：认领后别人拿不到；认领到期可被重新认领；
// 交还后可再认领；死信不再派发；尝试次数逐次累加。
func testOutboxClaim(t *testing.T, db store.Store) {
	ctx := context.Background()
	h := newCore().Snapshot().Header
	if err := db.CreateSession(ctx, store.SessionMeta{ID: "oc"}, h, nil); err != nil {
		t.Fatal(err)
	}
	cp := store.Checkpoint{SessionID: "oc", Header: h, RunState: "idle", Outbox: []store.OutboxItem{
		{ID: "c1", Envelope: []byte(`{}`), CreatedAt: 1}, {ID: "c2", Envelope: []byte(`{}`), CreatedAt: 2},
		{ID: "c3", Envelope: []byte(`{}`), CreatedAt: 3}}}
	if err := db.Commit(ctx, cp); err != nil {
		t.Fatal(err)
	}
	ids := func(xs []store.OutboxItem) string {
		var s []string
		for _, x := range xs {
			s = append(s, x.ID)
		}
		return strings.Join(s, ",")
	}
	a, err := db.ClaimOutbox(ctx, "A", time.Hour, 2)
	if err != nil || ids(a) != "c1,c2" || a[0].Attempts != 1 {
		t.Fatalf("A 认领 = %+v %v", a, err)
	}
	b, _ := db.ClaimOutbox(ctx, "B", time.Hour, 0)
	if ids(b) != "c3" {
		t.Fatalf("B 只应拿到未认领的 c3，得 %s", ids(b))
	}
	if more, _ := db.ClaimOutbox(ctx, "B", time.Hour, 0); len(more) != 0 {
		t.Fatalf("全部已认领时不应再拿到：%s", ids(more))
	}
	// 交还：只有认领者本人能交还。
	if err := db.ReleaseOutbox(ctx, "c1", "B"); err != nil {
		t.Fatal(err)
	}
	if again, _ := db.ClaimOutbox(ctx, "B", time.Hour, 0); len(again) != 0 {
		t.Fatalf("别人交还不算数：%s", ids(again))
	}
	if err := db.ReleaseOutbox(ctx, "c1", "A"); err != nil {
		t.Fatal(err)
	}
	if again, _ := db.ClaimOutbox(ctx, "B", time.Hour, 0); ids(again) != "c1" || again[0].Attempts != 2 {
		t.Fatalf("交还后应可再认领、尝试次数 2：%+v", again)
	}
	// 认领到期（派发者崩溃）：可被重新认领。
	if err := db.ReleaseOutbox(ctx, "c2", "A"); err != nil {
		t.Fatal(err)
	}
	if exp, _ := db.ClaimOutbox(ctx, "A", -time.Second, 0); ids(exp) != "c2" {
		t.Fatalf("应认领到 c2：%s", ids(exp))
	}
	if re, _ := db.ClaimOutbox(ctx, "C", time.Hour, 0); ids(re) != "c2" {
		t.Fatalf("到期的认领应可被重新认领：%s", ids(re))
	}
	// 死信：不再派发，也不出现在 Outbox 里。
	if err := db.DeadOutbox(ctx, "c3", "bad"); err != nil {
		t.Fatal(err)
	}
	if err := db.ReleaseOutbox(ctx, "c3", "B"); err != nil {
		t.Fatal(err)
	}
	for _, it := range func() []store.OutboxItem { x, _ := db.Outbox(ctx, 0); return x }() {
		if it.ID == "c3" {
			t.Fatal("死信不应出现在待派发里")
		}
	}
	if err := db.MarkOutbox(ctx, "c1"); err != nil {
		t.Fatal(err)
	}
	if done, _ := db.ClaimOutbox(ctx, "Z", -time.Second, 0); strings.Contains(ids(done), "c1") || strings.Contains(ids(done), "c3") {
		t.Fatalf("已派发 / 死信不应被认领：%s", ids(done))
	}
}

// —— 按子存储访问与 Tx（05：T3 落地） ——//

func testSubStoresAndTx(t *testing.T, db store.Store) {
	ctx := context.Background()
	c := newCore()
	create(t, db, store.SessionMeta{ID: "tx-1"}, c)
	ok := false
	err := db.Tx(ctx, func(tx store.Store) error {
		if admitted, err := tx.Admit(ctx, "tx-1", store.InboxItem{ItemID: "t1", Kind: store.InboxInput, Input: `[]`}); err != nil || !admitted {
			return err
		}
		if in, _ := tx.InInbox(ctx, "tx-1", "t1"); !in {
			t.Fatal("事务内的写入应立即可见")
		}
		if err := tx.Commit(ctx, store.Checkpoint{SessionID: "tx-1", ExpectLogLen: 0, Header: c.Snapshot().Header}); err != nil {
			return err
		}
		ok = true
		return nil
	})
	if err != nil || !ok {
		t.Fatalf("Tx: %v committed=%v", err, ok)
	}
	if items, err := db.Inbox(ctx, "tx-1"); err != nil || len(items) != 1 {
		t.Fatalf("提交后收件: %v %+v", err, items)
	}
	sentinel := errors.New("内部错误")
	if err := db.Tx(ctx, func(tx store.Store) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("Tx 应传播 fn 的错误: %v", err)
	}
	if _, err := db.Exists(ctx, "tx-1"); err != nil {
		t.Fatalf("Exists: %v", err)
	}
}

func testTraceEvents(t *testing.T, db store.Store) {
	ctx := context.Background()
	for _, ev := range []store.TraceEvent{
		{Trace: "t1", Session: "s1", Kind: "envelope", Detail: "human:u1 -> session:s1", At: 100},
		{Trace: "t1", Session: "s1", Kind: "tool", Detail: "echo ok", At: 200},
		{Trace: "t2", Session: "s2", Kind: "envelope", Detail: "other", At: 150},
	} {
		if err := db.RecordTrace(ctx, ev); err != nil {
			t.Fatalf("RecordTrace: %v", err)
		}
	}
	got, err := db.TraceEvents(ctx, "t1")
	if err != nil || len(got) != 2 {
		t.Fatalf("TraceEvents: %v %+v", err, got)
	}
	if got[0].Kind != "envelope" || got[1].Kind != "tool" || got[1].Detail != "echo ok" {
		t.Fatalf("TraceEvents order/content: %+v", got)
	}
	if got[0].Trace != "t1" || got[0].Session != "s1" {
		t.Fatalf("TraceEvents fields: %+v", got[0])
	}
	if empty, err := db.TraceEvents(ctx, "none"); err != nil || len(empty) != 0 {
		t.Fatalf("empty trace: %v %+v", err, empty)
	}
}

// testSleepingWithMail 休眠扫描一次查出有可唤醒信件的会话：前面一大批没信件的不挤掉后面有信件的，
// 只有背景信件的不算（2026-11-04 审查 P1-37）。存储未实现 SleepScanner 时跳过。
func testSleepingWithMail(t *testing.T, db store.Store) {
	sc, ok := db.(store.SleepScanner)
	if !ok {
		t.Skip("存储未实现 SleepScanner")
	}
	ctx := context.Background()
	sleep := func(id string) {
		t.Helper()
		create(t, db, store.SessionMeta{ID: id}, newCore())
		ep, err := db.AcquireLease(ctx, id, "n")
		if err != nil {
			t.Fatal(err)
		}
		if err := db.SetSessionStatus(ctx, id, store.StatusActive, store.StatusSleeping, ep); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 70; i++ {
		sleep(fmt.Sprintf("a%02d", i))
	}
	sleep("y")
	sleep("z")
	if _, err := db.Admit(ctx, "y", store.InboxItem{ItemID: "c1", Kind: store.InboxContext, Input: `[]`}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Admit(ctx, "z", store.InboxItem{ItemID: "w1", Kind: store.InboxInput, Input: `[]`}); err != nil {
		t.Fatal(err)
	}
	ids, err := sc.SleepingWithMail(ctx, []string{store.InboxInput, store.InboxNotice}, 64)
	if err != nil || len(ids) != 1 || ids[0] != "z" {
		t.Fatalf("应只查出有 wake 信件的 z：%v %v", ids, err)
	}
}

// —— 事务回滚与并发（2026-11-04 审查 P1-55）：第三方实现（如 Postgres）Tx 不原子、认领不互斥时这里会失败 ——//

func testTxRollback(t *testing.T, db store.Store) {
	ctx := context.Background()
	c := newCore()
	create(t, db, store.SessionMeta{ID: "rb"}, c)
	boom := errors.New("回滚")
	err := db.Tx(ctx, func(tx store.Store) error {
		if _, err := tx.Admit(ctx, "rb", store.InboxItem{ItemID: "r1", Kind: store.InboxInput, Input: `[]`}); err != nil {
			return err
		}
		if err := tx.CreateSession(ctx, store.SessionMeta{ID: "rb-2"}, c.Snapshot().Header, nil); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Tx 应返回 fn 的错误: %v", err)
	}
	if items, _ := db.Inbox(ctx, "rb"); len(items) != 0 {
		t.Fatalf("回滚后事务内的写入不应可见: %+v", items)
	}
	if ok, _ := db.Exists(ctx, "rb-2"); ok {
		t.Fatal("回滚后事务内建的会话不应存在")
	}
	// 回滚后存储照常可用。
	if fresh, err := db.Admit(ctx, "rb", store.InboxItem{ItemID: "r1", Kind: store.InboxInput, Input: `[]`}); err != nil || !fresh {
		t.Fatalf("回滚后同一 item 应能重新准入: %v %v", fresh, err)
	}
	// 嵌套 Tx 在外层事务里执行，不自锁；外层回滚连同嵌套里的写入一起撤销。
	done := make(chan error, 1)
	go func() {
		done <- db.Tx(ctx, func(tx store.Store) error {
			if err := tx.Tx(ctx, func(in store.Store) error {
				_, err := in.Admit(ctx, "rb", store.InboxItem{ItemID: "n1", Kind: store.InboxInput, Input: `[]`})
				return err
			}); err != nil {
				return err
			}
			return boom
		})
	}()
	select {
	case err := <-done:
		if !errors.Is(err, boom) {
			t.Fatalf("嵌套 Tx: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("嵌套 Tx 卡死")
	}
	if ok, _ := db.InInbox(ctx, "rb", "n1"); ok {
		t.Fatal("外层回滚应撤销嵌套事务里的写入")
	}
}

// run 并发跑 n 份 f，等全部结束。
func run(n int, f func(i int)) {
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); f(i) }(i)
	}
	wg.Wait()
}

func testConcurrency(t *testing.T, db store.Store) {
	ctx := context.Background()
	const n = 8
	h := newCore().Snapshot().Header
	if err := db.CreateSession(ctx, store.SessionMeta{ID: "cc"}, h, nil); err != nil {
		t.Fatal(err)
	}
	// 发件箱：并发认领，每条只被一个认领者拿到，合起来不漏。
	var out []store.OutboxItem
	for i := 0; i < 20; i++ {
		out = append(out, store.OutboxItem{ID: fmt.Sprintf("o%02d", i), Envelope: []byte(`{}`), CreatedAt: int64(i + 1)})
	}
	if err := db.Commit(ctx, store.Checkpoint{SessionID: "cc", Header: h, RunState: "idle", Outbox: out}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	seen := map[string]int{}
	run(n, func(i int) {
		got, err := db.ClaimOutbox(ctx, fmt.Sprintf("h%d", i), time.Hour, 5)
		if err != nil {
			t.Errorf("ClaimOutbox: %v", err)
			return
		}
		mu.Lock()
		for _, it := range got {
			seen[it.ID]++
		}
		mu.Unlock()
	})
	if len(seen) != len(out) {
		t.Fatalf("并发认领应合起来拿到全部 %d 条，得 %d", len(out), len(seen))
	}
	for id, k := range seen {
		if k != 1 {
			t.Fatalf("%s 被认领了 %d 次", id, k)
		}
	}
	// 回信核对：同一 corr 并发回信，恰好一个成功。
	if err := db.IssueCorr(ctx, store.Pending{Corr: "cc-corr", SessionID: "cc", ReplyTo: "agent:w"}); err != nil {
		t.Fatal(err)
	}
	var claimed int32
	run(n, func(int) {
		if _, err := db.ClaimCorr(ctx, "cc-corr", "cc", "agent:w"); err == nil {
			mu.Lock()
			claimed++
			mu.Unlock()
		}
	})
	if claimed != 1 {
		t.Fatalf("同一 corr 应恰好核对成功一次，得 %d", claimed)
	}
	// 信箱：同一 item 并发准入，恰好一次 fresh。
	var fresh int32
	run(n, func(int) {
		ok, err := db.Admit(ctx, "cc", store.InboxItem{ItemID: "dup", Kind: store.InboxInput, Input: `[]`})
		if err == nil && ok {
			mu.Lock()
			fresh++
			mu.Unlock()
		}
	})
	if fresh != 1 {
		t.Fatalf("同一 item 并发准入应恰好一次 fresh，得 %d", fresh)
	}
	// 检查点：同一 ExpectLogLen 并发提交，恰好一个成功，其余 ErrConflict。
	c := newCore()
	create(t, db, store.SessionMeta{ID: "cc-commit"}, c)
	c.Input(txt("a"), nil)
	s := c.Snapshot()
	var ok, conflict int32
	run(n, func(int) {
		err := db.Commit(ctx, store.Checkpoint{SessionID: "cc-commit", ExpectLogLen: 0, AppendFrom: 0,
			Append: s.Entries, Header: s.Header, RunState: "running"})
		mu.Lock()
		defer mu.Unlock()
		switch {
		case err == nil:
			ok++
		case errors.Is(err, store.ErrConflict):
			conflict++
		default:
			t.Errorf("并发 Commit 意外错误: %v", err)
		}
	})
	if ok != 1 || conflict != n-1 {
		t.Fatalf("同一 ExpectLogLen 并发提交：成功 %d、冲突 %d，期望 1 / %d", ok, conflict, n-1)
	}
}

// lookup 读工具记录视图（原 ToolResultStore.LookupTool，审查 P1-54 起由调用方用 GetTool 组合）。
func lookup(ctx context.Context, db interface {
	GetTool(ctx context.Context, sessionID, callID string) (*store.ToolRecord, error)
}, sid, cid string) (store.ToolRecord, bool, error) {
	r, err := db.GetTool(ctx, sid, cid)
	if err != nil || r == nil {
		return store.ToolRecord{}, false, err
	}
	return *r, true, nil
}

// testCheckpointEvents 检查点附带的持久事件（06 10.2.1 规则 2）：与条目同事务写入，排在条目之后各占一个游标，
// 回放时 data 原样；提交通知与回放一致。
func testCheckpointEvents(t *testing.T, db store.Store) {
	ctx := context.Background()
	c := newCore()
	create(t, db, store.SessionMeta{ID: "ev"}, c)
	_ = c.Record(txt("a"), nil)
	var got []bus.Envelope
	db.OnCommit(func(e bus.Envelope) { got = append(got, e) })
	defer db.OnCommit(nil)
	commitAll(t, db, "ev", c, 0, func(cp *store.Checkpoint) {
		cp.Events = []store.Event{{Kind: "session.mode_changed", Data: json.RawMessage(`{"mode":"ask"}`)},
			{Kind: "session.state_changed", Data: json.RawMessage(`{"run_state":"running"}`)}}
	})
	evs, wm, err := db.Replay(ctx, "ev", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 3 || wm != 3 {
		t.Fatalf("回放 %d 条 wm=%d，期望 3 条（条目 + 两个事件）", len(evs), wm)
	}
	if evs[0].Kind != store.EventEntry || evs[1].Kind != "session.mode_changed" || evs[1].Seq != 2 || evs[2].Seq != 3 {
		t.Fatalf("顺序不对：%s/%d %s/%d %s/%d", evs[0].Kind, evs[0].Seq, evs[1].Kind, evs[1].Seq, evs[2].Kind, evs[2].Seq)
	}
	if d, _ := evs[1].Data.(json.RawMessage); !bytes.Contains(d, []byte(`"ask"`)) {
		t.Fatalf("事件 data 应原样回放：%v", evs[1].Data)
	}
	if len(got) != 3 || got[2].Kind != "session.state_changed" || got[2].Seq != 3 {
		t.Fatalf("提交通知应与回放一致：%+v", got)
	}
	// 冲突的检查点：事件也不写。
	if err := db.Commit(ctx, store.Checkpoint{SessionID: "ev", ExpectLogLen: 99, Header: c.Snapshot().Header,
		Events: []store.Event{{Kind: "x", Data: json.RawMessage(`{}`)}}}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("应冲突：%v", err)
	}
	if evs, _, _ := db.Replay(ctx, "ev", 3); len(evs) != 0 {
		t.Fatalf("冲突的检查点不应写事件：%+v", evs)
	}
}

// testEventsCursorExpired 事件按保留期清理（06 10.12 Q3）：清理后早于被清游标的回放返回 ErrCursorExpired，
// 从被清的最大游标往后订阅仍可用。
func testEventsCursorExpired(t *testing.T, db store.Store) {
	ctx := context.Background()
	pr, ok := db.(store.PruneStore)
	if !ok {
		t.Skip("存储未实现可选的 PruneStore")
	}
	c := newCore()
	create(t, db, store.SessionMeta{ID: "ex"}, c)
	_ = c.Record(txt("a"), nil)
	_ = c.Record(txt("b"), nil)
	commitAll(t, db, "ex", c, 0, nil)
	if _, err := pr.Prune(ctx, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.Replay(ctx, "ex", 0); !errors.Is(err, store.ErrCursorExpired) {
		t.Fatalf("游标 0 早于已清理的事件，应 ErrCursorExpired：%v", err)
	}
	evs, wm, err := db.Replay(ctx, "ex", 2)
	if err != nil || len(evs) != 0 || wm != 2 {
		t.Fatalf("从最大被清游标往后订阅应可用：%d %d %v", len(evs), wm, err)
	}
}

// testInteractions 交互（06 10.6）：登记与结束各写一条持久事件，子会话的交互同时写进根会话；
// 只有第一次结束生效，之后返回已有结果；结束后 corr 回信核对不过；未结束的交互能按根会话列出。
func testInteractions(t *testing.T, db store.Store) {
	ctx := context.Background()
	create(t, db, store.SessionMeta{ID: "r"}, newCore())
	create(t, db, store.SessionMeta{ID: "r/c", ParentID: "r", Origin: "subagent"}, newCore())
	kinds := func(sid string) []string {
		t.Helper()
		evs, _, err := db.Replay(ctx, sid, 0)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range evs {
			out = append(out, e.Kind)
		}
		return out
	}
	p := store.Pending{Corr: "corr-i", SessionID: "r/c", Root: "r", InteractionID: "i1", Kind: "approval",
		Payload: json.RawMessage(`{"id":"i1","session":"r/c"}`)}
	if err := db.OpenInteraction(ctx, p, 0); err != nil {
		t.Fatal(err)
	}
	for _, sid := range []string{"r/c", "r"} {
		if ks := kinds(sid); len(ks) != 1 || ks[0] != store.EventInteractionOpened {
			t.Fatalf("%s 应有一条 interaction_opened：%v", sid, ks)
		}
	}
	open, err := db.OpenInteractions(ctx, "r")
	if err != nil || len(open) != 1 || open[0].InteractionID != "i1" {
		t.Fatalf("按根会话应列出子会话的交互：%+v %v", open, err)
	}
	got, won, err := db.ResolveInteraction(ctx, "i1", store.Resolution{Outcome: "approved", By: "local", ClientID: "k1"})
	if err != nil || !won || got.Outcome != "approved" || got.By != "local" {
		t.Fatalf("第一次结束应生效：%+v %v %v", got, won, err)
	}
	got, won, err = db.ResolveInteraction(ctx, "i1", store.Resolution{Outcome: "denied", By: "other"})
	if err != nil || won || got.Outcome != "approved" || got.ClientID != "k1" {
		t.Fatalf("第二次结束不生效、返回已有结果：%+v %v %v", got, won, err)
	}
	for _, sid := range []string{"r/c", "r"} {
		if ks := kinds(sid); len(ks) != 2 || ks[1] != store.EventInteractionResolved {
			t.Fatalf("%s 应再有一条 interaction_resolved：%v", sid, ks)
		}
	}
	if _, err := db.ClaimCorr(ctx, "corr-i", "r/c", ""); !errors.Is(err, store.ErrCorrRejected) {
		t.Fatalf("交互结束后回信应被拒：%v", err)
	}
	if open, _ := db.OpenInteractions(ctx, ""); len(open) != 0 {
		t.Fatalf("结束后不应再列出：%+v", open)
	}
	if _, _, err := db.ResolveInteraction(ctx, "none", store.Resolution{Outcome: "voided"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("不存在的交互应 ErrNotFound：%v", err)
	}
	if err := db.OpenInteraction(ctx, store.Pending{Corr: "corr-j", SessionID: "r", InteractionID: "i2"}, 999); !errors.Is(err, store.ErrLeaseLost) {
		t.Fatalf("纪元不符应 ErrLeaseLost：%v", err)
	}
}

// —— 会话行元数据（session.update）——//

func testSessionMetaUpdate(t *testing.T, st store.Store) {
	ctx := context.Background()
	db, ok := st.(store.SessionMetaStore)
	if !ok {
		t.Skip("存储未实现可选的 SessionMetaStore")
	}
	c := newCore()
	create(t, st, store.SessionMeta{ID: "s1", Title: "旧", Meta: json.RawMessage(`{"client_id":"c"}`)}, c)
	time.Sleep(2 * time.Millisecond)
	create(t, st, store.SessionMeta{ID: "s2"}, newCore())
	before, _ := st.ListSessions(ctx, 0, 10, false)
	var got []bus.Envelope
	st.OnCommit(func(e bus.Envelope) { got = append(got, e) })
	title := "新标题"
	if err := db.UpdateSessionMeta(ctx, "s1", &title, map[string]any{"pinned": true}); err != nil {
		t.Fatal(err)
	}
	st.OnCommit(nil)
	l, err := st.Load(ctx, "s1", nil)
	if err != nil || l.Meta.Title != "新标题" {
		t.Fatalf("标题 %+v %v", l, err)
	}
	var m map[string]any
	_ = json.Unmarshal(l.Meta.Meta, &m)
	if m["pinned"] != true || m["client_id"] != "c" {
		t.Fatalf("业务键合并 %s", l.Meta.Meta)
	}
	if len(got) != 1 || got[0].Kind != store.EventMeta || got[0].Seq != l.Cursor {
		t.Fatalf("meta 事件 %+v cursor=%d", got, l.Cursor)
	}
	evs, _, _ := st.Replay(ctx, "s1", 0)
	if len(evs) != 1 || evs[0].Kind != store.EventMeta {
		t.Fatalf("回放 %+v", evs)
	}
	after, _ := st.ListSessions(ctx, 0, 10, false)
	if len(after) != 2 || after[0].ID != before[0].ID || after[1].UpdatedAt != before[1].UpdatedAt {
		t.Fatalf("改元数据不应改变列表顺序: %+v → %+v", before, after)
	}
	for _, x := range after {
		if x.ID == "s1" && !strings.Contains(string(x.Meta), `"pinned":true`) {
			t.Fatalf("列表行带业务键: %s", x.Meta)
		}
	}
	pinned, err := db.SessionsWithMeta(ctx, "pinned", 10)
	if err != nil || len(pinned) != 1 || pinned[0].ID != "s1" || pinned[0].Title != "新标题" {
		t.Fatalf("置顶列表 %+v %v", pinned, err)
	}
	if err := db.UpdateSessionMeta(ctx, "s1", nil, map[string]any{"pinned": nil}); err != nil {
		t.Fatal(err)
	}
	if pinned, _ := db.SessionsWithMeta(ctx, "pinned", 10); len(pinned) != 0 {
		t.Fatalf("取消置顶 %+v", pinned)
	}
	if l, _ := st.Load(ctx, "s1", nil); l.Meta.Title != "新标题" {
		t.Fatalf("title=nil 不应改标题: %q", l.Meta.Title)
	}
	if err := db.UpdateSessionMeta(ctx, "none", &title, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("不存在应 ErrNotFound: %v", err)
	}
}
