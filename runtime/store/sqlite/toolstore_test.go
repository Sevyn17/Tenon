package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestConfirmLifecycle(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	if err := db.PendConfirm(ctx, "s", "c", "h1", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.PendConfirm(ctx, "s", "c", "h1", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("重复挂起应幂等: %v", err)
	}
	if err := db.StartTool(ctx, "s", "c", "h1"); !errors.Is(err, ErrStateMismatch) {
		t.Fatalf("未批准不能 started: %v", err)
	}
	if err := db.Deliver(ctx, "s", "c", "", "approve"); err != nil {
		t.Fatal(err)
	}
	if err := db.StartTool(ctx, "s", "c", "h2"); !errors.Is(err, ErrStateMismatch) {
		t.Fatalf("参数哈希不同不能 started: %v", err)
	}
	if err := db.StartTool(ctx, "s", "c", "h1"); err != nil {
		t.Fatalf("批准后应可 started: %v", err)
	}
	if err := db.FinishTool(ctx, "s", "c", ToolStarted, `{"text":"ok"}`); err != nil {
		t.Fatal(err)
	}
	r, ok, err := lookup(ctx, db, "s", "c")
	if err != nil || !ok || r.Status != ToolDone || r.Decision != "approve" {
		t.Fatalf("终态: %+v %v %v", r, ok, err)
	}
	if err := db.StartTool(ctx, "s", "fresh", "h"); err != nil {
		t.Fatalf("无记录应插入 started: %v", err)
	}
}

func TestPendConfirmRefreshesHash(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	if err := db.PendConfirm(ctx, "s", "c", "old", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.PendConfirm(ctx, "s", "c", "new", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.Deliver(ctx, "s", "c", "", "approve"); err != nil {
		t.Fatal(err)
	}
	if err := db.StartTool(ctx, "s", "c", "new"); err != nil {
		t.Fatal(err)
	}
}

// lookup 读工具记录视图（原 ToolResultStore.LookupTool，审查 P1-54 起由调用方用 GetTool 组合）。
func lookup(ctx context.Context, db interface {
	GetTool(ctx context.Context, sessionID, callID string) (*ToolRecord, error)
}, sid, cid string) (ToolRecord, bool, error) {
	r, err := db.GetTool(ctx, sid, cid)
	if err != nil || r == nil {
		return ToolRecord{}, false, err
	}
	return *r, true, nil
}
