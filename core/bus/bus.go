// Package bus：事件总线（event-bus 定稿 D1–D5）。
//
// 两种通道：
//   - Subscribe：易失通道，只收订阅之后发布的事件；每个订阅者一个有界队列，
//     满了就断开（不静默丢单条），Err() 给出断点 seq（D2）。
//   - Follow：可靠通道，先从存储回放 seq > after 的事件，发一个 Synced 标记，
//     再无缝转入实时（D1）。需要完整性的消费者只用它。
//
// 总线永不阻塞发布者（D4）；订阅随作用域结束自动注销（D5）。
package bus

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Version 信封当前版本：同版本只加字段；改语义升版本（D3）。
const Version = 1

// KindSynced Follow 回放完毕的标记事件。
const KindSynced = "synced"

// Envelope 事件信封（D3）。Seq 在会话内单调（等于条目的 sync_seq；易失事件为 0）。
type Envelope struct {
	V       int    `json:"v"`
	Seq     int64  `json:"seq"`
	At      int64  `json:"at"`
	Session string `json:"session"`
	Kind    string `json:"kind"`
	Data    any    `json:"data,omitempty"`
}

// Filter 订阅过滤：Session 为空表示全部会话；Kinds 为空表示全部类型。
type Filter struct {
	Session string
	Kinds   []string
}

func (f Filter) match(e Envelope) bool {
	if f.Session != "" && e.Session != f.Session {
		return false
	}
	if len(f.Kinds) == 0 || e.Kind == KindSynced {
		return true
	}
	for _, k := range f.Kinds {
		if k == e.Kind {
			return true
		}
	}
	return false
}

// ErrSlow 订阅者跟不上被断开；Breakpoint 是它收到的最后一个 seq，用 Follow(after=Breakpoint) 补齐。
type ErrSlow struct{ Breakpoint int64 }

func (e *ErrSlow) Error() string {
	return fmt.Sprintf("bus: 订阅者过慢已断开（断点 seq=%d）", e.Breakpoint)
}

// ErrClosed 订阅随作用域或总线关闭而结束。
var ErrClosed = errors.New("bus: 订阅已关闭")

// Source Follow 的回放来源（存储）：返回 seq > after 的持久事件（按 seq 升序）与回放水位。
type Source interface {
	Replay(ctx context.Context, session string, after int64) (events []Envelope, watermark int64, err error)
}

// Bus 进程内事件总线。
type Bus struct {
	mu     sync.Mutex
	subs   map[*Sub]struct{}
	cap    int
	now    func() time.Time
	closed bool
}

// New 构造；queue 为每个订阅者的队列容量（默认 256）。
func New(queue int) *Bus {
	if queue <= 0 {
		queue = 256
	}
	return &Bus{subs: map[*Sub]struct{}{}, cap: queue, now: time.Now}
}

// Sub 一个订阅。
type Sub struct {
	b    *Bus
	f    Filter
	ch   chan Envelope
	mu   sync.Mutex
	last int64
	err  error
	done bool
	gate func(Envelope) bool // Follow 用：实时事件先过闸
}

// C 事件通道；关闭后读 Err()。
func (s *Sub) C() <-chan Envelope { return s.ch }

// Err 订阅结束原因（nil 表示仍在进行）。
func (s *Sub) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// Close 主动注销。
func (s *Sub) Close() { s.b.drop(s, ErrClosed) }

// Publish 发布（先落库再发布由调用方保证）。不阻塞：慢订阅者被断开。
func (b *Bus) Publish(e Envelope) {
	if e.V == 0 {
		e.V = Version
	}
	if e.At == 0 {
		e.At = b.now().UnixMilli()
	}
	b.mu.Lock()
	subs := make([]*Sub, 0, len(b.subs))
	for s := range b.subs {
		subs = append(subs, s)
	}
	b.mu.Unlock()
	for _, s := range subs {
		if !s.f.match(e) {
			continue
		}
		s.offer(e)
	}
}

func (s *Sub) offer(e Envelope) {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return
	}
	if s.gate != nil && !s.gate(e) {
		s.mu.Unlock()
		return
	}
	select {
	case s.ch <- e:
		if e.Seq > s.last {
			s.last = e.Seq
		}
		s.mu.Unlock()
	default:
		bp := s.last
		s.mu.Unlock()
		s.b.drop(s, &ErrSlow{Breakpoint: bp})
	}
}

func (b *Bus) drop(s *Sub, why error) {
	b.mu.Lock()
	delete(b.subs, s)
	b.mu.Unlock()
	s.mu.Lock()
	if !s.done {
		s.done, s.err = true, why
		close(s.ch)
	}
	s.mu.Unlock()
}

func (b *Bus) add(ctx context.Context, f Filter) *Sub {
	s := &Sub{b: b, f: f, ch: make(chan Envelope, b.cap)}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		s.done, s.err = true, ErrClosed
		close(s.ch)
		return s
	}
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	if ctx != nil {
		go func() { <-ctx.Done(); b.drop(s, ErrClosed) }()
	}
	return s
}

// Subscribe 易失订阅；ctx 结束（作用域关闭）自动注销。
func (b *Bus) Subscribe(ctx context.Context, f Filter) *Sub { return b.add(ctx, f) }

// Follow 可靠订阅：回放 seq > after → Synced → 实时，不漏不重。
// 只接收带 Seq 的持久事件；f.Session 必填。
func (b *Bus) Follow(ctx context.Context, src Source, f Filter, after int64) (*Sub, error) {
	if f.Session == "" {
		return nil, errors.New("bus: Follow 必须指定会话")
	}
	// 先挂上订阅、把实时事件缓存起来，再读存储，避免回放与实时之间的缝隙。
	var (
		mu      sync.Mutex
		buf     []Envelope
		syncing = true
		water   int64
	)
	s := &Sub{b: b, f: f, ch: make(chan Envelope, b.cap)}
	s.gate = func(e Envelope) bool {
		if e.Seq == 0 { // 易失事件不进可靠通道
			return false
		}
		mu.Lock()
		defer mu.Unlock()
		if syncing {
			buf = append(buf, e)
			return false
		}
		return e.Seq > water
	}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	if ctx != nil {
		go func() { <-ctx.Done(); b.drop(s, ErrClosed) }()
	}

	events, wm, err := src.Replay(ctx, f.Session, after)
	if err != nil {
		b.drop(s, err)
		return nil, err
	}
	out := make([]Envelope, 0, len(events)+1)
	for _, e := range events {
		if e.Seq > after && f.match(e) {
			out = append(out, e)
		}
	}
	out = append(out, Envelope{V: Version, Seq: wm, At: b.now().UnixMilli(), Session: f.Session, Kind: KindSynced})
	// 回放与缓存一起按序送出；送完之前实时事件继续进缓存，保证顺序。
	mu.Lock()
	water = wm
	mu.Unlock()
	for {
		mu.Lock()
		for _, e := range buf {
			if e.Seq > water {
				out = append(out, e)
				water = e.Seq
			}
		}
		buf = nil
		if len(out) == 0 {
			syncing = false
			mu.Unlock()
			break
		}
		mu.Unlock()
		s.sendAll(out)
		out = out[:0]
	}
	return s, nil
}

// sendAll 按序送出；队列满则断开。
func (s *Sub) sendAll(out []Envelope) {
	for _, e := range out {
		s.mu.Lock()
		if s.done {
			s.mu.Unlock()
			break
		}
		select {
		case s.ch <- e:
			if e.Seq > s.last {
				s.last = e.Seq
			}
			s.mu.Unlock()
		default:
			bp := s.last
			s.mu.Unlock()
			s.b.drop(s, &ErrSlow{Breakpoint: bp})
			return
		}
	}
}

// Close 关闭总线，结束全部订阅。
func (b *Bus) Close() {
	b.mu.Lock()
	b.closed = true
	subs := make([]*Sub, 0, len(b.subs))
	for s := range b.subs {
		subs = append(subs, s)
	}
	b.mu.Unlock()
	for _, s := range subs {
		b.drop(s, ErrClosed)
	}
}

// Len 当前订阅数（测试与 explain 用）。
func (b *Bus) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}
