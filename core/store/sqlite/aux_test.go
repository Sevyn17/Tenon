package sqlite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestToolResultsStateMachine(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()

	// 有副作用：started → done；重复 started 不覆盖。
	if err := db.PutToolStarted(ctx, "s1", "c1", "h1"); err != nil {
		t.Fatal(err)
	}
	if err := db.PutToolStarted(ctx, "s1", "c1", "h1"); !errors.Is(err, ErrStateMismatch) {
		t.Fatalf("重复 started 应拒绝: %v", err)
	}
	if err := db.FinishTool(ctx, "s1", "c1", ToolDelivered, "x"); !errors.Is(err, ErrStateMismatch) {
		t.Fatalf("错误的期望状态应拒绝: %v", err)
	}
	if err := db.FinishTool(ctx, "s1", "c1", ToolStarted, `"ok"`); err != nil {
		t.Fatal(err)
	}
	r, _ := db.GetTool(ctx, "s1", "c1")
	if r == nil || r.Status != ToolDone || r.Output != `"ok"` || r.ArgsHash != "h1" {
		t.Fatalf("记录不符: %+v", r)
	}

	// 挂起：pending → delivered → done；主键带会话，不同会话同 call_id 互不影响。
	dl := time.Now().Add(time.Hour)
	if err := db.PutToolPending(ctx, ToolRecord{SessionID: "s1", CallID: "c2", Kind: "confirm", ArgsHash: "h2", Deadline: dl}); err != nil {
		t.Fatal(err)
	}
	if err := db.PutToolStarted(ctx, "s2", "c2", ""); err != nil {
		t.Fatalf("另一会话同 call_id 应允许: %v", err)
	}
	ps, _ := db.PendingTools(ctx, "s1")
	if len(ps) != 1 || ps[0].CallID != "c2" || ps[0].Deadline.UnixMilli() != dl.UnixMilli() {
		t.Fatalf("挂起列表不符: %+v", ps)
	}
	if err := db.Deliver(ctx, "s1", "c2", "", "approve"); err != nil {
		t.Fatal(err)
	}
	if err := db.Deliver(ctx, "s1", "c2", "", "approve"); !errors.Is(err, ErrStateMismatch) {
		t.Fatalf("重复送达应拒绝: %v", err)
	}
	if err := db.FinishTool(ctx, "s1", "c2", ToolDelivered, `"done"`); err != nil {
		t.Fatal(err)
	}

	// 只读工具直接 done。
	if err := db.FinishTool(ctx, "s1", "c3", "", `"r"`); err != nil {
		t.Fatal(err)
	}

	// 清理不碰 pending。
	_ = db.PutToolPending(ctx, ToolRecord{SessionID: "s1", CallID: "c4", Kind: "result"})
	if err := db.DeleteFinishedTools(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	if r, _ := db.GetTool(ctx, "s1", "c1"); r != nil {
		t.Fatalf("done 应被清理")
	}
	if r, _ := db.GetTool(ctx, "s1", "c4"); r == nil || r.Status != ToolPending {
		t.Fatalf("pending 不应被清理")
	}
}

func TestBlobsDedupVerifyGC(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "blobs")
	b, err := db.NewBlobs(root)
	if err != nil {
		t.Fatal(err)
	}
	h1, err := b.Put(ctx, "s1", "text/plain", []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	h2, _ := b.Put(ctx, "s2", "text/plain", []byte("hello"))
	if h1 != h2 {
		t.Fatalf("内容寻址应去重")
	}
	if got, err := b.Get(h1); err != nil || string(got) != "hello" {
		t.Fatalf("Get: %q %v", got, err)
	}
	// 外部改坏 → 校验失败。
	os.WriteFile(filepath.Join(root, h1), []byte("evil"), 0o644)
	if _, err := b.Get(h1); err == nil {
		t.Fatalf("校验应失败")
	}

	// 无引用 + 过宽限期才回收。
	h3, _ := b.Put(ctx, "", "", []byte("orphan"))
	if n, _ := b.GC(ctx, time.Hour); n != 0 {
		t.Fatalf("宽限期内不应回收")
	}
	if n, err := b.GC(ctx, -time.Second); err != nil || n != 1 {
		t.Fatalf("应回收 1 个孤儿: %d %v", n, err)
	}
	if _, err := os.Stat(filepath.Join(root, h3)); !os.IsNotExist(err) {
		t.Fatalf("孤儿文件应删除")
	}
	if _, err := os.Stat(filepath.Join(root, h1)); err != nil {
		t.Fatalf("有引用的不应删除")
	}
}

func TestDefinitionsVersioned(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	if _, _, err := db.GetDefinition(ctx, "agent", "a", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("应 NotFound: %v", err)
	}
	v1, _ := db.PutDefinition(ctx, "agent", "a", "one")
	v2, _ := db.PutDefinition(ctx, "agent", "a", "two")
	if v1 != 1 || v2 != 2 {
		t.Fatalf("版本号: %d %d", v1, v2)
	}
	if body, v, _ := db.GetDefinition(ctx, "agent", "a", 0); body != "two" || v != 2 {
		t.Fatalf("最新: %s %d", body, v)
	}
	if body, _, _ := db.GetDefinition(ctx, "agent", "a", 1); body != "one" {
		t.Fatalf("旧版本应保留: %s", body)
	}
}

func TestInboxIdempotentAcrossConsume(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	c := newCore()
	if err := db.CreateSession(ctx, SessionMeta{ID: "s1"}, c.Snapshot().Header, nil); err != nil {
		t.Fatal(err)
	}
	ok, err := db.Admit(ctx, "s1", InboxItem{ItemID: "i1", Kind: InboxInput, Input: `[]`})
	if err != nil || !ok {
		t.Fatalf("首次准入: %v %v", ok, err)
	}
	if ok, _ := db.Admit(ctx, "s1", InboxItem{ItemID: "i1", Kind: InboxInput, Input: `[]`}); ok {
		t.Fatal("待处理时重放应被拒")
	}
	_, _ = db.Admit(ctx, "s1", InboxItem{ItemID: "i2", Kind: InboxNote, Input: `[]`})
	items, _ := db.Inbox(ctx, "s1")
	if len(items) != 2 || items[0].ItemID != "i1" {
		t.Fatalf("顺序: %+v", items)
	}
	if err := db.Commit(ctx, Checkpoint{SessionID: "s1", Header: c.Snapshot().Header, ConsumeInbox: []string{"i1"}}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := db.Admit(ctx, "s1", InboxItem{ItemID: "i1", Kind: InboxInput, Input: `[]`}); ok {
		t.Fatal("已消费后重放仍应被拒（D6）")
	}
	items, _ = db.Inbox(ctx, "s1")
	if len(items) != 1 || items[0].ItemID != "i2" {
		t.Fatalf("消费后: %+v", items)
	}
}
