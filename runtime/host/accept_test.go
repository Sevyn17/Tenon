package host

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/runtime/store"
)

// M4.9：回合结束（inbox 已消费）后重放同一 item id，不再开回合。
func TestReplayAfterConsumed(t *testing.T) {
	r := newRig(t, nil, &fakeModel{})
	hd, err := r.h.Open(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := hd.Do(Command{Kind: Send, ItemID: "same", Text: "hi"}); err != nil {
			t.Fatal(err)
		}
		if err := hd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	if r.m.calls != 1 || len(messages(t, hd)) != 2 {
		t.Fatalf("重放应只开一个回合: calls=%d msgs=%d", r.m.calls, len(messages(t, hd)))
	}
}

// M4.9：两个进程（两个 Host、同一 DB 文件）写同一会话：后写者被拒，已提交的日志不被破坏。
// T2a-1 起第二个 Host 打开会话即接管更新的租约纪元（02 2.2 fencing），旧写者得到 ErrLeaseLost。
func TestSecondHostConflicts(t *testing.T) {
	r1 := newRig(t, nil, &fakeModel{})
	r2 := newRig(t, r1.db, &fakeModel{})
	ctx := context.Background()
	h1, err := r1.h.Open(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	h2, err := r2.h.Open(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	rejected := func(e error) bool {
		return errors.Is(e, store.ErrConflict) || errors.Is(e, store.ErrLeaseLost)
	}
	// inbox 在共享库里，两个 actor 都可能取到同一输入；后打开者持新纪元租约，另一个的写必须被拒。
	if err := h1.Do(Command{Kind: Send, Text: "a"}); err != nil {
		t.Fatal(err)
	}
	e1 := h1.Wait()
	e2 := h2.Wait()
	conflicts := 0
	for _, e := range []error{e1, e2} {
		switch {
		case e == nil:
		case rejected(e):
			conflicts++
		default:
			t.Fatalf("意外错误: %v", e)
		}
	}
	if conflicts == 0 { // 未撞上：没跑回合的那个（状态过期）再写一次必然被拒
		stale := h2
		if r1.m.calls == 0 { // h2 先取到输入并提交，h1 才是过期写者
			stale = h1
		}
		if err := stale.Do(Command{Kind: Send, Text: "b"}); err != nil && !errors.Is(err, ErrClosed) {
			t.Fatalf("过期写者投递失败: %v", err)
		}
		select {
		case <-stale.Done():
		case <-time.After(10 * time.Second):
			t.Fatalf("过期写者 10s 内未退出")
		}
		if !rejected(stale.Err()) {
			t.Fatalf("过期写者应因写冲突/租约失效退出: %v", stale.Err())
		}
	} else if conflicts != 1 {
		t.Fatalf("应恰好一个写者被拒: %v / %v", e1, e2)
	}
	l, err := r1.db.Load(ctx, "s", nil)
	if err != nil {
		t.Fatal(err)
	}
	if l.Snapshot.Header.LogLen != 2 {
		t.Fatalf("已提交日志应保持 2 条（第一写者的回合），得 %d", l.Snapshot.Header.LogLen)
	}
}
