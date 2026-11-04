package bus

import (
	"context"
	"runtime"
	"testing"
	"time"
)

// Sub.Close 之后，盯 ctx 的协程要退出（2026-11-04 审查 P2 资源：原先要等 ctx 结束才退出，
// 长寿命 ctx 上反复订阅 / 注销会一直累积协程）。
func TestSubCloseReleasesWatcher(t *testing.T) {
	b := New(0)
	defer b.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	before := runtime.NumGoroutine()
	for i := 0; i < 200; i++ {
		b.Subscribe(ctx, Filter{}).Close()
	}
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before+10 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > before+10 {
		t.Fatalf("注销后协程未退出：前 %d，后 %d", before, n)
	}
}
