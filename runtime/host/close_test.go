package host

import (
	"sync"
	"testing"
	"time"
)

// D5 的根因回归：后台派发协程用 context.Background()，不随调用方 context 结束。
// Close 之后不能再起新的派发，否则测试在 t.Cleanup 里关库后它还会访问数据库，
// 与 t.TempDir 的清理撞上（CI 上偶发 "directory not empty"）。
func TestHostCloseStopsFlush(t *testing.T) {
	h := &Host{}
	h.Close()
	// 关闭后踢派发不应起协程，也不应 panic。
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			h.kickFlush()
		}
	}()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close 后 kickFlush 不应阻塞或起协程")
	}
	h.flushMu.Lock()
	flushing := h.flushing
	h.flushMu.Unlock()
	if flushing {
		t.Fatal("Close 后不应有派发在跑")
	}
}

// Close 幂等：重复调用不出错。
func TestHostCloseIdempotent(t *testing.T) {
	h := &Host{}
	h.Close()
	h.Close()
	h.flushMu.Lock()
	closed := h.closed
	h.flushMu.Unlock()
	if !closed {
		t.Fatal("Close 应置位 closed")
	}
}
