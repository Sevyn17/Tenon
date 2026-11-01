package plugin

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestScopeCleanupOrderAndCascade(t *testing.T) {
	root := NewRoot("proc")
	var order []string
	root.OnClose(func(context.Context) error { order = append(order, "root-1"); return nil })
	sess := root.Child("session")
	sess.OnClose(func(context.Context) error { order = append(order, "sess-1"); return nil })
	sess.OnClose(func(context.Context) error { order = append(order, "sess-2"); return nil })
	turn := sess.Child("turn")
	turn.OnClose(func(context.Context) error { order = append(order, "turn"); return nil })
	root.OnClose(func(context.Context) error { order = append(order, "root-2"); return nil })

	if root.Live() != 2 {
		t.Fatalf("Live=%d", root.Live())
	}
	if err := root.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"turn", "sess-2", "sess-1", "root-2", "root-1"}
	if len(order) != len(want) {
		t.Fatalf("order=%v", order)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order=%v", order)
		}
	}
	if turn.Context().Err() == nil {
		t.Fatal("子作用域 context 应被取消")
	}
	if root.Live() != 0 {
		t.Fatal("关闭后不应残留子作用域")
	}
}

func TestScopeChildCloseDetaches(t *testing.T) {
	root := NewRoot("p")
	c := root.Child("s")
	_ = c.Close(context.Background())
	if root.Live() != 0 {
		t.Fatal("子作用域关闭后应从父移除")
	}
	_ = c.Close(context.Background()) // 幂等
	late := false
	c.OnClose(func(context.Context) error { late = true; return nil })
	if !late {
		t.Fatal("已关闭作用域上注册清理应立即执行")
	}
}

func TestScopeGoRestartThenDegraded(t *testing.T) {
	root := NewRoot("p")
	root.backoff = time.Millisecond
	var runs atomic.Int32
	root.Go("flaky", func(context.Context) error { runs.Add(1); return errors.New("boom") })
	deadline := time.Now().Add(2 * time.Second)
	for len(root.Degraded()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := runs.Load(); got != MaxRestarts+1 {
		t.Fatalf("runs=%d", got)
	}
	if d := root.Degraded(); len(d) != 1 || d[0] != "flaky" {
		t.Fatalf("degraded=%v", d)
	}
	_ = root.Close(context.Background())
}

func TestScopeGoPanicRecoveredAndCancelledOnClose(t *testing.T) {
	root := NewRoot("p")
	root.backoff = time.Millisecond
	var runs atomic.Int32
	root.Go("p", func(ctx context.Context) error {
		if runs.Add(1) == 1 {
			panic("x")
		}
		<-ctx.Done()
		return nil
	})
	for runs.Load() < 2 {
		time.Sleep(time.Millisecond)
	}
	done := make(chan struct{})
	go func() { _ = root.Close(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close 应取消并等待后台任务")
	}
	if len(root.Degraded()) != 0 {
		t.Fatal("panic 后重启成功不应 degraded")
	}
}
