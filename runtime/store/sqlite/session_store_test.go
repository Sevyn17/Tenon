package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"ybase/runtime/session"
	"ybase/runtime/store"
)

func txt(s string) session.Content { return session.Content{session.NewTextPart(s)} }

func newCore() *session.Core {
	return session.New(&session.Config{Model: "m", System: []session.SystemBlock{{Text: "sys", Source: "agent"}},
		Tools: []session.ToolSpec{{Name: "echo", Schema: json.RawMessage(`{"type":"object"}`)}}, Policy: session.DefaultPolicy()})
}

// commitAll 把内核当前快照中 [from, end) 的条目随头部提交。
func commitAll(t *testing.T, db *DB, id string, c *session.Core, expect int, extra func(*store.Checkpoint)) int {
	t.Helper()
	s := c.Snapshot()
	from := expect
	cp := store.Checkpoint{SessionID: id, ExpectLogLen: expect, AppendFrom: from,
		Append: s.Entries[from-s.Base:], Header: s.Header, RunState: "running"}
	if extra != nil {
		extra(&cp)
	}
	if err := db.Commit(context.Background(), cp); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	return s.Header.LogLen
}

func TestSessionRoundTripAndRestore(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	c := newCore()
	if err := db.CreateSession(ctx, store.SessionMeta{ID: "s1"}, c.Snapshot().Header, nil); err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, n1, _ := c.Input(txt("你好"), nil)
	n := commitAll(t, db, "s1", c, 0, func(cp *store.Checkpoint) {
		cp.Request = &store.RequestRecord{RequestID: n1.Model.RequestID, ShellHash: "x", PrefixEnd: 1, FinalHash: "f",
			Composition: json.RawMessage(`{"system":3}`)}
		cp.Spans = []store.Span{{ID: "sp1", Kind: "step", Start: time.Now()}}
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
	if len(l.Snapshot.Entries) != 2 {
		t.Fatalf("条目数 %d", len(l.Snapshot.Entries))
	}
	if _, err := session.Restore(l.Snapshot); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	r, err := db.GetRequest(ctx, "s1", n1.Model.RequestID)
	if err != nil || r == nil || r.FinalHash != "f" || string(r.Composition) != `{"system":3}` {
		t.Fatalf("请求记录: %+v %v", r, err)
	}
	var sum string
	db.SQL().QueryRow(`SELECT summary FROM session_entries WHERE idx = 1`).Scan(&sum)
	if sum != "嗨" {
		t.Fatalf("summary = %q", sum)
	}
}

func TestCommitConditionalWrite(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	c := newCore()
	db.CreateSession(ctx, store.SessionMeta{ID: "s"}, c.Snapshot().Header, nil)
	c.Input(txt("a"), nil)
	commitAll(t, db, "s", c, 0, nil)
	// 第二个写者用旧的期望值
	s := c.Snapshot()
	err := db.Commit(ctx, store.Checkpoint{SessionID: "s", ExpectLogLen: 0, Header: s.Header})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("应冲突: %v", err)
	}
}

func TestTruncateArchivesAndSyncSeqMonotonic(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	c := newCore()
	db.CreateSession(ctx, store.SessionMeta{ID: "s"}, c.Snapshot().Header, nil)
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
	var arch int
	db.SQL().QueryRow(`SELECT count(*) FROM session_entries_archive WHERE session_id='s'`).Scan(&arch)
	if arch != 1 {
		t.Fatalf("归档条数 %d", arch)
	}
	var seqB, seqC int
	db.SQL().QueryRow(`SELECT sync_seq FROM session_entries_archive`).Scan(&seqB)
	db.SQL().QueryRow(`SELECT sync_seq FROM session_entries WHERE idx = 1`).Scan(&seqC)
	_ = seqB
	var oldMax int
	db.SQL().QueryRow(`SELECT max(sync_seq) FROM session_entries WHERE idx = 0`).Scan(&oldMax)
	if seqC <= 2 { // a=1, b=2，截断占 3，c=4
		t.Fatalf("撤销后 sync_seq 复用: %d", seqC)
	}
	es, _ := db.Entries(ctx, "s", 0, -1)
	if len(es) != 2 || es[1].Item.User.Content[0].Text != "c" {
		t.Fatalf("条目不符: %+v", es)
	}
}
