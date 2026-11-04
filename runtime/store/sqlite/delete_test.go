package sqlite

import (
	"context"
	"errors"
	"testing"
	"ybase/runtime/store"
)

func TestDeleteSessionCallsCleanersAndTombstoneOnFailure(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	c := newCore()
	if err := db.CreateSession(ctx, store.SessionMeta{ID: "s1"}, c.Snapshot().Header, nil); err != nil {
		t.Fatal(err)
	}
	_, _, _ = c.Input(txt("hi"), nil)
	commitAll(t, db, "s1", c, 0, nil)

	boom := errors.New("boom")
	if err := db.DeleteSession(ctx, "s1", []store.Cleaner{func(context.Context, string) error { return boom }}); !errors.Is(err, boom) {
		t.Fatalf("回调失败应返回错误: %v", err)
	}
	if _, err := db.Load(ctx, "s1", nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("墓碑会话应不可见: %v", err)
	}
	var called []string
	if err := db.DeleteSession(ctx, "s1", []store.Cleaner{func(_ context.Context, id string) error { called = append(called, id); return nil }}); err != nil {
		t.Fatalf("重试删除: %v", err)
	}
	if len(called) != 1 || called[0] != "s1" {
		t.Fatalf("回调未调用: %v", called)
	}
	var n int
	db.SQL().QueryRow(`SELECT COUNT(*) FROM session_entries WHERE session_id='s1'`).Scan(&n)
	var m int
	db.SQL().QueryRow(`SELECT COUNT(*) FROM sessions WHERE id='s1'`).Scan(&m)
	if n != 0 || m != 0 {
		t.Fatalf("核心表未清理: entries=%d sessions=%d", n, m)
	}
}
