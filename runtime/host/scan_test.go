package host

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
)

// 兜底扫描不被前面一大批没信件的休眠会话挤掉（2026-11-04 审查 P1-37：原先每次只取 id 最小的 64 个，
// 后面有信件的会话永远扫不到）。
func TestScanFindsMailBehindManySleepers(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	sleep := func(id string) {
		t.Helper()
		c := session.New(&session.Config{Model: "m", Policy: session.DefaultPolicy()})
		if err := r.db.CreateSession(ctx, store.SessionMeta{ID: id}, c.Snapshot().Header, nil); err != nil {
			t.Fatal(err)
		}
		ep, err := r.db.AcquireLease(ctx, id, "other")
		if err != nil {
			t.Fatal(err)
		}
		if err := r.db.SetSessionStatus(ctx, id, store.StatusActive, store.StatusSleeping, ep); err != nil {
			t.Fatal(err)
		}
		_ = r.db.ReleaseLease(ctx, id, "other", ep)
	}
	for i := 0; i < 70; i++ {
		sleep(fmt.Sprintf("a%02d", i))
	}
	sleep("z")
	if _, err := r.db.Admit(ctx, "z", store.InboxItem{ItemID: "w1", Kind: store.InboxInput, Input: `[{"kind":"text","text":"hi"}]`}); err != nil {
		t.Fatal(err)
	}
	n, err := r.h.ScanOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, open := r.h.Get("z"); n != 1 || !open {
		t.Fatalf("应唤醒 z：woke=%d", n)
	}
}
