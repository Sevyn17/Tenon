package host

// 04 4.3 Turn 中间件在真实 Host 上：包住一轮、可 Skip、恢复时重跑且 Resumed=true。

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Sevyn17/tenon/runtime/component"
)

// 中间件包住每一轮：一次用户消息跑一轮，链跑一次。
func TestTurnMWWrapsEachTurn(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	var mu sync.Mutex
	var turns []component.TurnReq
	r.h.TurnChain = func(kernel func(context.Context, component.TurnReq) (component.TurnResp, error)) func(context.Context, component.TurnReq) (component.TurnResp, error) {
		return func(ctx context.Context, req component.TurnReq) (component.TurnResp, error) {
			mu.Lock()
			turns = append(turns, req)
			mu.Unlock()
			return kernel(ctx, req)
		}
	}
	hd, err := r.h.Open(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	_ = hd.Wait()
	if err := hd.Do(Command{Kind: Send, ItemID: NewItemID(), Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	_ = hd.Wait()
	mu.Lock()
	n := len(turns)
	first := turns[0]
	mu.Unlock()
	if n == 0 {
		t.Fatal("Turn 链应被调用")
	}
	if first.SessionID != "s1" {
		t.Fatalf("应拿到会话 id：%+v", first)
	}
	if first.Resumed {
		t.Fatalf("正常回合不该是 Resumed：%+v", first)
	}
}

// 中间件能改写 End：把回合结束原因换掉。
func TestTurnMWRewritesEnd(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	r.h.TurnChain = func(kernel func(context.Context, component.TurnReq) (component.TurnResp, error)) func(context.Context, component.TurnReq) (component.TurnResp, error) {
		return func(ctx context.Context, req component.TurnReq) (component.TurnResp, error) {
			resp, err := kernel(ctx, req)
			if err != nil {
				return resp, err
			}
			// 改写成一个明确的结束原因（Kind 用内核的 EndDone 值 0）。
			resp.End = []byte(`{"Kind":0}`)
			return resp, nil
		}
	}
	hd, err := r.h.Open(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	_ = hd.Wait()
	if err := hd.Do(Command{Kind: Send, ItemID: NewItemID(), Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatalf("改写 End 后不该报错：%v", err)
	}
}
