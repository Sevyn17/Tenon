package host

// 04 4.3「Turn 不跨挂起」：一轮挂起后恢复时，Turn 链重新跑一次且 Resumed=true。

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/runtime/component"
)

// 崩溃恢复：换宿主打开同一会话，未完成回合的 Turn 链重跑且 Resumed=true。
func TestTurnMWRerunOnResume(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.db")
	r := newRig(t, path, &fakeModel{})
	var mu sync.Mutex
	var reqs []component.TurnReq
	r.h.TurnChain = func(kernel func(context.Context, component.TurnReq) (component.TurnResp, error)) func(context.Context, component.TurnReq) (component.TurnResp, error) {
		return func(ctx context.Context, req component.TurnReq) (component.TurnResp, error) {
			mu.Lock()
			reqs = append(reqs, req)
			mu.Unlock()
			return kernel(ctx, req)
		}
	}
	hd, err := r.h.Open(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	// 发一条要调工具的消息，让它停在「等确认」（= 未完成回合）。
	_ = hd.Do(Command{Kind: Send, Text: "用 tool"})
	callID := waitConfirm(t, r.db, "s1")

	mu.Lock()
	before := len(reqs)
	mu.Unlock()
	if before == 0 {
		t.Fatal("第一轮应跑过链")
	}

	// 模拟进程被杀：关库，换一个宿主打开同一会话。
	r.db.Close()
	r2 := newRig(t, path, &fakeModel{})
	r2.h.TurnChain = func(kernel func(context.Context, component.TurnReq) (component.TurnResp, error)) func(context.Context, component.TurnReq) (component.TurnResp, error) {
		return func(ctx context.Context, req component.TurnReq) (component.TurnResp, error) {
			mu.Lock()
			reqs = append(reqs, req)
			mu.Unlock()
			return kernel(ctx, req)
		}
	}
	hd2, err := r2.h.Open(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if id2 := waitConfirm(t, r2.db, "s1"); id2 != callID {
		t.Fatalf("恢复后应仍在等同一调用：%s vs %s", id2, callID)
	}
	// 恢复是异步的（actor 在后台跑）：等链被重跑，不要立刻读。
	// 4.3 硬约束：恢复时 Turn 链重跑一次，且 Resumed=true。
	deadline := time.Now().Add(10 * time.Second)
	for {
		mu.Lock()
		after := len(reqs)
		mu.Unlock()
		if after > before {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("恢复时链应重跑：before=%d after=%d", before, after)
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, req := range reqs[before:] {
		if req.Resumed {
			found = true
		}
	}
	if !found {
		t.Fatalf("恢复那一轮 Resumed 应为 true：%+v", reqs[before:])
	}
	_ = hd2
}
