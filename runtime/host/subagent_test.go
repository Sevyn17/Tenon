package host

import (
	"context"
	"path/filepath"
	"testing"

	"ybase/runtime/agent"
	"ybase/runtime/session"
)

func TestSpawnSubagent(t *testing.T) {
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	ctx := context.Background()
	if _, err := r.h.Open(ctx, "p"); err != nil {
		t.Fatal(err)
	}
	out, err := r.h.Spawn(ctx, "p", "p/sub-1", "", r.h.New, "查一下")
	if err != nil {
		t.Fatal(err)
	}
	if out != "re:查一下" {
		t.Fatalf("结果 = %q", out)
	}
	l, err := r.db.Load(ctx, "p/sub-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if l.Meta.ParentID != "p" || l.Meta.Origin != "subagent" {
		t.Fatalf("会话行 = %+v", l.Meta)
	}
	// 续用同一子会话：第二回合
	out, err = r.h.Spawn(ctx, "p", "p/sub-1", "", r.h.New, "再查")
	if err != nil || out != "re:再查" {
		t.Fatalf("续用: %q %v", out, err)
	}
	if n := len(messages(t, mustGet(t, r.h, "p/sub-1"))); n != 4 {
		t.Fatalf("子会话应有 4 条消息，得到 %d", n)
	}
	// 别的父会话不能续用
	if _, err := r.h.Spawn(ctx, "q", "p/sub-1", "", r.h.New, "x"); err == nil {
		t.Fatal("非本会话的子智能体应被拒绝")
	}
}

// 子会话关闭后被 Open 重新打开（休眠唤醒、重启后收信、control 都走 Open）仍按 Child 构造；
// 普通会话仍按 New（审查 P0-5：原先一律 New，委派工具重新可见、深度限制失效）。
func TestReopenedChildUsesChildFactory(t *testing.T) {
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	ctx := context.Background()
	var built []string
	newF := r.h.New
	r.h.New = func(id, def string) (*agent.Agent, *session.Config, error) {
		built = append(built, "new:"+id)
		return newF(id, def)
	}
	r.h.Child = func(id, def string) (*agent.Agent, *session.Config, error) {
		built = append(built, "child:"+id)
		return newF(id, def)
	}
	if _, err := r.h.Open(ctx, "p"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.h.Spawn(ctx, "p", "p/sub-1", "", r.h.Child, "查"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"p", "p/sub-1"} {
		hd, ok := r.h.Get(id)
		if !ok {
			continue // Spawn 结束后子会话可能已关
		}
		if err := hd.Do(Command{Kind: Close}); err != nil {
			t.Fatal(err)
		}
		<-hd.Done()
	}
	built = nil
	for _, id := range []string{"p/sub-1", "p"} {
		if _, err := r.h.Open(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if len(built) != 2 || built[0] != "child:p/sub-1" || built[1] != "new:p" {
		t.Fatalf("重开时的构造方 = %v，期望 [child:p/sub-1 new:p]", built)
	}
}

func mustGet(t *testing.T, h *Host, id string) Handle {
	t.Helper()
	hd, ok := h.Get(id)
	if !ok {
		t.Fatalf("会话 %s 未打开", id)
	}
	return hd
}
