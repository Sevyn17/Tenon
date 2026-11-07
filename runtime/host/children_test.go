package host

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"tenon/runtime/agent"
	"tenon/runtime/session"
	"tenon/runtime/store"
)

// 父会话回退时取消子会话：全库会话很多时较老的子会话也要被取消
// （2026-11-04 审查 P1-38：原先扫 ListSessions(…, 500)，按更新时间倒序最多 500 条，老子会话漏掉）。
func TestCancelChildrenFindsOldChild(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	mustOpen(t, r.h, "p")
	if _, err := r.h.open(ctx, "p/old", "", r.h.New, func(ag *agent.Agent, cfg *session.Config) error {
		_, err := ag.OpenAs(ctx, cfg, store.SessionMeta{ID: "p/old", ParentID: "p", Origin: "subagent"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	// 全库超过 500 个会话、且都比子会话更新：ListSessions 最多返回 500 条，子会话不在其中。
	for i := 0; i < 510; i++ {
		c := session.New(&session.Config{Model: "m", Policy: session.DefaultPolicy()})
		if err := r.db.CreateSession(ctx, store.SessionMeta{ID: fmt.Sprintf("x%03d", i)}, c.Snapshot().Header, nil); err != nil {
			t.Fatal(err)
		}
	}
	r.h.cancelChildren(ctx, "p")
	waitFor(t, func() bool { _, ok := r.h.Get("p/old"); return !ok })
}
