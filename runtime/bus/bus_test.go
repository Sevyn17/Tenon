package bus

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// memSource：测试用存储，持久事件按 seq 保存。
type memSource struct {
	mu  sync.Mutex
	evs []Envelope
}

func (m *memSource) add(e Envelope) {
	m.mu.Lock()
	m.evs = append(m.evs, e)
	m.mu.Unlock()
}

func (m *memSource) Replay(_ context.Context, session string, after int64) ([]Envelope, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Envelope
	var wm int64
	for _, e := range m.evs {
		if e.Session == session && e.Seq > after {
			out = append(out, e)
		}
		if e.Seq > wm {
			wm = e.Seq
		}
	}
	return out, wm, nil
}

func recv(t *testing.T, s *Sub) Envelope {
	t.Helper()
	select {
	case e, ok := <-s.C():
		if !ok {
			t.Fatalf("通道已关闭: %v", s.Err())
		}
		return e
	case <-time.After(2 * time.Second):
		t.Fatal("超时未收到事件")
	}
	return Envelope{}
}

func TestSubscribeFilterAndEnvelope(t *testing.T) {
	b := New(8)
	s := b.Subscribe(context.Background(), Filter{Session: "s1", Kinds: []string{"item"}})
	b.Publish(Envelope{Session: "s2", Kind: "item"})
	b.Publish(Envelope{Session: "s1", Kind: "delta"})
	b.Publish(Envelope{Session: "s1", Kind: "item", Seq: 3})
	e := recv(t, s)
	if e.Seq != 3 || e.V != Version || e.At == 0 {
		t.Fatalf("信封不符: %+v", e)
	}
}

func TestSlowSubscriberDisconnectedWithBreakpoint(t *testing.T) {
	b := New(2)
	s := b.Subscribe(context.Background(), Filter{Session: "s1"})
	for i := int64(1); i <= 3; i++ {
		b.Publish(Envelope{Session: "s1", Kind: "item", Seq: i})
	}
	var got []int64
	for e := range s.C() {
		got = append(got, e.Seq)
	}
	var slow *ErrSlow
	if !errors.As(s.Err(), &slow) || slow.Breakpoint != 2 || len(got) != 2 {
		t.Fatalf("应在断点 2 断开: got=%v err=%v", got, s.Err())
	}
	if b.Len() != 0 {
		t.Fatal("断开后应注销")
	}
}

func TestSubscriptionEndsWithScope(t *testing.T) {
	b := New(4)
	ctx, cancel := context.WithCancel(context.Background())
	s := b.Subscribe(ctx, Filter{})
	cancel()
	for range s.C() {
	}
	if !errors.Is(s.Err(), ErrClosed) {
		t.Fatalf("err=%v", s.Err())
	}
	deadline := time.Now().Add(time.Second)
	for b.Len() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if b.Len() != 0 {
		t.Fatal("作用域结束后应注销")
	}
}

func TestFollowReplayThenLiveNoGapNoDup(t *testing.T) {
	b := New(1024)
	src := &memSource{}
	pub := func(seq int64) {
		e := Envelope{Session: "s1", Kind: "item", Seq: seq}
		src.add(e) // 先落库
		b.Publish(e)
	}
	for i := int64(1); i <= 5; i++ {
		pub(i)
	}
	// 回放期间并发继续发布，检验衔接处不漏不重。
	done := make(chan struct{})
	go func() {
		for i := int64(6); i <= 200; i++ {
			pub(i)
		}
		close(done)
	}()
	s, err := b.Follow(context.Background(), src, Filter{Session: "s1"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	<-done
	pub(201)
	var seqs []int64
	synced := false
	for len(seqs) < 199 {
		e := recv(t, s)
		if e.Kind == KindSynced {
			synced = true
			continue
		}
		seqs = append(seqs, e.Seq)
	}
	if !synced {
		t.Fatal("应收到 Synced 标记")
	}
	for i, q := range seqs {
		if q != int64(i)+3 {
			t.Fatalf("第 %d 个 seq=%d，期望 %d（不漏不重、按序）", i, q, i+3)
		}
	}
}

// 回放量远超队列容量：消费者在 Follow 返回后才开始读，不能因此被判过慢断开；
// 回放期间的实时发布不阻塞发布者，且在 Synced 之后按序衔接（审查 P0-9：原先回放 > 容量必然 ErrSlow）。
func TestFollowReplayLargerThanQueue(t *testing.T) {
	b := New(8)
	src := &memSource{}
	pub := func(seq int64) {
		e := Envelope{Session: "s1", Kind: "item", Seq: seq}
		src.add(e)
		b.Publish(e)
	}
	for i := int64(1); i <= 300; i++ {
		src.add(Envelope{Session: "s1", Kind: "item", Seq: i})
	}
	s, err := b.Follow(context.Background(), src, Filter{Session: "s1"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	// 消费者还没读：这时发布不能阻塞（D4）。
	published := make(chan struct{})
	go func() {
		for i := int64(301); i <= 320; i++ {
			pub(i)
		}
		close(published)
	}()
	select {
	case <-published:
	case <-time.After(2 * time.Second):
		t.Fatal("回放期间发布者被阻塞")
	}
	var seqs []int64
	synced := false
	for len(seqs) < 320 {
		e := recv(t, s)
		if e.Kind == KindSynced {
			synced = true
			continue
		}
		seqs = append(seqs, e.Seq)
	}
	if !synced || s.Err() != nil {
		t.Fatalf("应收到 Synced 且订阅仍有效: synced=%v err=%v", synced, s.Err())
	}
	for i, q := range seqs {
		if q != int64(i)+1 {
			t.Fatalf("第 %d 个 seq=%d，期望 %d", i, q, i+1)
		}
	}
	// 回放中途注销：后台送端退出，通道关闭。
	s2, err := b.Follow(context.Background(), src, Filter{Session: "s1"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	recv(t, s2)
	s2.Close()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-s2.C():
			if !ok {
				if !errors.Is(s2.Err(), ErrClosed) {
					t.Fatalf("注销原因应为 ErrClosed: %v", s2.Err())
				}
				return
			}
		case <-deadline:
			t.Fatal("注销后通道应关闭")
		}
	}
}

func TestFollowIgnoresVolatileAndRequiresSession(t *testing.T) {
	b := New(8)
	src := &memSource{}
	if _, err := b.Follow(context.Background(), src, Filter{}, 0); err == nil {
		t.Fatal("Follow 不指定会话应报错")
	}
	s, err := b.Follow(context.Background(), src, Filter{Session: "s1"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if e := recv(t, s); e.Kind != KindSynced {
		t.Fatalf("首个应为 Synced: %+v", e)
	}
	b.Publish(Envelope{Session: "s1", Kind: "delta"}) // 易失（Seq=0）
	b.Publish(Envelope{Session: "s1", Kind: "item", Seq: 1})
	if e := recv(t, s); e.Seq != 1 {
		t.Fatalf("易失事件不应进入可靠通道: %+v", e)
	}
}

func TestBusClose(t *testing.T) {
	b := New(4)
	s := b.Subscribe(context.Background(), Filter{})
	b.Close()
	for range s.C() {
	}
	if !errors.Is(s.Err(), ErrClosed) {
		t.Fatalf("err=%v", s.Err())
	}
	s2 := b.Subscribe(context.Background(), Filter{})
	if _, ok := <-s2.C(); ok || !errors.Is(s2.Err(), ErrClosed) {
		t.Fatal("关闭后的订阅应立即结束")
	}
}

// Live：回放完毕后也送易失事件；Tree：子会话的事件只送实时，且不推进断点（游标属于别的序列）。
func TestFollowLiveAndTree(t *testing.T) {
	b := New(16)
	src := &memSource{}
	src.add(Envelope{Session: "r", Kind: "item", Seq: 1})
	s, err := b.Follow(context.Background(), src, Filter{Session: "r", Tree: true, Live: true}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if e := recv(t, s); e.Seq != 1 {
		t.Fatalf("应先回放 seq 1：%+v", e)
	}
	if e := recv(t, s); e.Kind != KindSynced {
		t.Fatalf("回放后应 synced：%+v", e)
	}
	b.Publish(Envelope{Session: "r", Kind: "delta"})            // 易失
	b.Publish(Envelope{Session: "r/c", Kind: "item", Seq: 99})  // 子会话持久事件
	b.Publish(Envelope{Session: "other", Kind: "item", Seq: 5}) // 别的会话：不收
	b.Publish(Envelope{Session: "r", Kind: "item", Seq: 2})
	want := []string{"r:delta:0", "r/c:item:99", "r:item:2"}
	for _, w := range want {
		e := recv(t, s)
		if got := e.Session + ":" + e.Kind + ":" + itoa(e.Seq); got != w {
			t.Fatalf("应收到 %s，得到 %s", w, got)
		}
	}
	s.mu.Lock()
	last := s.last
	s.mu.Unlock()
	if last != 2 {
		t.Fatalf("断点只按本会话的游标推进：%d", last)
	}
	// 不带 Live 的 Follow 仍只收本会话的持久事件。
	s2, _ := b.Follow(context.Background(), src, Filter{Session: "r", Tree: true}, 2)
	recv(t, s2) // synced
	b.Publish(Envelope{Session: "r", Kind: "delta"})
	b.Publish(Envelope{Session: "r/c", Kind: "item", Seq: 100})
	b.Publish(Envelope{Session: "r", Kind: "item", Seq: 3})
	if e := recv(t, s2); e.Seq != 3 || e.Session != "r" {
		t.Fatalf("非 Live 只收本会话持久事件：%+v", e)
	}
}

func itoa(n int64) string { return fmt.Sprint(n) }
