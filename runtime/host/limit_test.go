package host

import (
	"context"
	"testing"
	"time"
)

// 并发许可池在首次使用时按 Limit 建好，之后改 Limit 不再新建（2026-11-04 审查 P2 资源：原先改了 Limit
// 就换一个新池，老池里的持有者照常占着，新旧两池加起来超过上限）。
func TestLimitFixedAfterFirstUse(t *testing.T) {
	h := &Host{Limit: 1}
	a := &actor{h: h, exit: make(chan struct{})}
	release, err := a.acquireSlot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	h.Limit = 5
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if r, err := a.acquireSlot(ctx); err == nil {
		r()
		t.Fatal("许可池已满时改 Limit 不应多放一个回合进来")
	}
}
