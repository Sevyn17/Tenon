package bus

import (
	"context"
	"errors"
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
