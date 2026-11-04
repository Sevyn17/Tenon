package store

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type fakePrune struct{ n atomic.Int32 }

func (f *fakePrune) Prune(context.Context, time.Time) (int, error) { f.n.Add(1); return 0, nil }

// Maintain 每轮都调 Prune 与 GC，ctx 取消即返回（从 host.Host 移来，2026-11-04 审查 P2 归属）。
func TestMaintainRunsPruneAndGC(t *testing.T) {
	p := &fakePrune{}
	var gc atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Maintain(ctx, MaintainOptions{Store: p, GC: func(context.Context) (int, error) { gc.Add(1); return 0, nil },
			Every: 20 * time.Millisecond})
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for p.n.Load() < 2 || gc.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("Prune %d 次、GC %d 次，应各至少 2 次", p.n.Load(), gc.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
}

// 既不能 Prune 也没有 GC 时直接返回，不起空转循环。
func TestMaintainNothingToDo(t *testing.T) {
	done := make(chan struct{})
	go func() { Maintain(context.Background(), MaintainOptions{Store: struct{}{}}); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("没有可做的清理时应立即返回")
	}
}
