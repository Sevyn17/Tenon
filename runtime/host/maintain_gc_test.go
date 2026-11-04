package host

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// Maintain 每轮都调 GC（回收没有引用的工件文件；2026-11-04 审查 P1-41：原先工件存储的 GC 没有调用方）。
func TestMaintainRunsGC(t *testing.T) {
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	var n atomic.Int32
	r.h.GC = func(context.Context) (int, error) { n.Add(1); return 0, nil }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.h.Maintain(ctx, 20*time.Millisecond, 0); close(done) }()
	waitFor(t, func() bool { return n.Load() >= 2 })
	cancel()
	<-done
}
