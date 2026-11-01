package host

import (
	"context"
	"path/filepath"
	"testing"
)

func TestSpawnSubagent(t *testing.T) {
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	ctx := context.Background()
	if _, err := r.h.Open(ctx, "p"); err != nil {
		t.Fatal(err)
	}
	out, err := r.h.Spawn(ctx, "p", "p/sub-1", r.h.New, "查一下")
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
	out, err = r.h.Spawn(ctx, "p", "p/sub-1", r.h.New, "再查")
	if err != nil || out != "re:再查" {
		t.Fatalf("续用: %q %v", out, err)
	}
	if n := len(messages(t, mustGet(t, r.h, "p/sub-1"))); n != 4 {
		t.Fatalf("子会话应有 4 条消息，得到 %d", n)
	}
	// 别的父会话不能续用
	if _, err := r.h.Spawn(ctx, "q", "p/sub-1", r.h.New, "x"); err == nil {
		t.Fatal("非本会话的子智能体应被拒绝")
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
