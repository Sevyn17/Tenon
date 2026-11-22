package host

// K3（Mortise docs/62）：回合结束钩子里排队投递「继续」是否安全。
// AfterTurn 在 actor 自己的 goroutine 里同步调用；钩子里 Handle.Do(Send{Queue:true})
// 只落信箱、往带缓冲的 done 写确认、非阻塞唤醒，不等 actor，所以不会卡死。
// 这里核实：不卡死、不丢、会话自动开下一轮、调用方计轮后停下（不会无限递归）。

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/runtime/session"
)

func TestAfterTurnQueuedSendContinues(t *testing.T) {
	r := newRig(t, nil, &fakeModel{})
	const max = 3
	var mu sync.Mutex
	turns := 0
	var hookErr error
	r.h.Hooks.AfterTurn = func(id string, end session.EndReason) {
		mu.Lock()
		turns++
		n := turns
		mu.Unlock()
		if n >= max { // 调用方计轮：到上限不再续跑
			return
		}
		hd, ok := r.h.Get(id)
		if !ok {
			mu.Lock()
			hookErr = context.Canceled
			mu.Unlock()
			return
		}
		if err := hd.Do(Command{Kind: Send, Text: "continue", Source: "system", Queue: true}); err != nil {
			mu.Lock()
			hookErr = err
			mu.Unlock()
		}
	}
	hd := mustOpen(t, r.h, "s1")
	if err := hd.Do(Command{Kind: Send, ItemID: "a", Text: "go"}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- hd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("钩子内排队投递卡死")
	}
	// Wait 返回时可能续跑的那一轮还没被 actor 拣起（barrier 与唤醒命令先后），再等到计数稳定。
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := turns
		mu.Unlock()
		if n >= max || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	n, herr := turns, hookErr
	mu.Unlock()
	if herr != nil {
		t.Fatalf("钩子内投递失败：%v", herr)
	}
	if n != max {
		t.Fatalf("回合数 %d，应为 %d", n, max)
	}
	got := roleLine(t, hd)
	want := "user:go | assistant:re:go | user:continue | assistant:re:continue | user:continue | assistant:re:continue"
	if got != want {
		t.Fatalf("历史：\n got %s\nwant %s", got, want)
	}
	if q, _ := r.h.Queued(context.Background(), "s1"); len(q) != 0 {
		t.Fatalf("不应还有排队项：%+v", q)
	}
	// 再等一会儿，确认没有多跑（不会无限续跑）。
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	n = turns
	mu.Unlock()
	if n != max || strings.Count(roleLine(t, hd), "user:") != max {
		t.Fatalf("到上限后仍在续跑：%d 轮", n)
	}
}
