package resmgr

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// counter 记 Open / close 次数的假实例。
type counter struct {
	opens, closes atomic.Int32
	delay         time.Duration
	fail          error
}

func (c *counter) def(id string, sc Scope) Def {
	return Def{Kind: "test", ID: id, Scope: sc, Open: func(context.Context) (any, func(), error) {
		if c.delay > 0 {
			time.Sleep(c.delay)
		}
		if c.fail != nil {
			return nil, nil, c.fail
		}
		n := c.opens.Add(1)
		return fmt.Sprintf("%s#%d", id, n), func() { c.closes.Add(1) }, nil
	}}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	dl := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(dl) {
			t.Fatalf("等不到：%s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

var wsA, wsB = Owner{Workspace: "a"}, Owner{Workspace: "b"}

func TestSameKeyReused(t *testing.T) {
	m := New(Options{Idle: time.Hour})
	defer m.Close()
	c := &counter{}
	d := c.def("x", "")
	l1, err := m.Acquire(context.Background(), d, wsA)
	if err != nil {
		t.Fatal(err)
	}
	l2, err := m.Acquire(context.Background(), d, wsA)
	if err != nil {
		t.Fatal(err)
	}
	if c.opens.Load() != 1 || l1.Value() != l2.Value() {
		t.Fatalf("同键应复用：opens=%d %v %v", c.opens.Load(), l1.Value(), l2.Value())
	}
	if ls := m.List(); len(ls) != 1 || ls[0].Refs != 2 || ls[0].Key.Scope != ScopeWorkspace || ls[0].Key.Tenant != LocalTenant {
		t.Fatalf("List = %+v", ls)
	}
	l1.Release()
	l1.Release() // 重复交还不多减
	if ls := m.List(); ls[0].Refs != 1 {
		t.Fatalf("refs = %d", ls[0].Refs)
	}
}

func TestWorkspaceScopePerWorkspace(t *testing.T) {
	m := New(Options{Idle: time.Hour})
	defer m.Close()
	c := &counter{}
	d := c.def("x", ScopeWorkspace)
	la, _ := m.Acquire(context.Background(), d, wsA)
	lb, _ := m.Acquire(context.Background(), d, wsB)
	if c.opens.Load() != 2 || la.Value() == lb.Value() {
		t.Fatalf("不同工作区应各一份：opens=%d", c.opens.Load())
	}
	if ls := m.List(); len(ls) != 2 || ls[0].Key.Workspace != "a" || ls[1].Key.Workspace != "b" {
		t.Fatalf("List = %+v", ls)
	}
}

func TestGlobalScopeShared(t *testing.T) {
	m := New(Options{Idle: time.Hour})
	defer m.Close()
	c := &counter{}
	d := c.def("g", ScopeGlobal)
	la, _ := m.Acquire(context.Background(), d, wsA)
	lb, _ := m.Acquire(context.Background(), d, Owner{Workspace: "b", Tenant: "t2"})
	if c.opens.Load() != 1 || la.Value() != lb.Value() {
		t.Fatalf("全局范围应跨工作区共用：opens=%d", c.opens.Load())
	}
	if k := la.Key(); k.Workspace != "" || k.Tenant != "" {
		t.Fatalf("全局键不应带工作区 / 租户：%+v", k)
	}
}

func TestTenantAndSessionScope(t *testing.T) {
	m := New(Options{Idle: time.Hour})
	defer m.Close()
	c := &counter{}
	dt := c.def("t", ScopeTenant)
	m.Acquire(context.Background(), dt, wsA)
	m.Acquire(context.Background(), dt, wsB) // 同租户 local：共用
	m.Acquire(context.Background(), dt, Owner{Tenant: "other", Workspace: "a"})
	if n := c.opens.Load(); n != 2 {
		t.Fatalf("租户范围：opens=%d，应 2", n)
	}
	ds := (&counter{}).def("s", ScopeSession)
	if _, err := m.Acquire(context.Background(), ds, wsA); err == nil {
		t.Fatal("会话范围没有会话应报错")
	}
	if _, err := m.Acquire(context.Background(), (&counter{}).def("w", ""), Owner{}); err == nil {
		t.Fatal("工作区范围没有工作区应报错")
	}
	if _, err := ParseScope("bogus"); err == nil {
		t.Fatal("非法范围应报错")
	}
}

func TestIdleReclaim(t *testing.T) {
	m := New(Options{Idle: 50 * time.Millisecond})
	defer m.Close()
	c := &counter{}
	d := c.def("x", "")
	l, _ := m.Acquire(context.Background(), d, wsA)
	l.Release()
	if ls := m.List(); len(ls) != 1 || ls[0].Refs != 0 || ls[0].IdleSince.IsZero() {
		t.Fatalf("引用归零后应先空闲着：%+v", ls)
	}
	waitFor(t, "空闲回收", func() bool { return len(m.List()) == 0 })
	if c.closes.Load() != 1 {
		t.Fatalf("closes=%d", c.closes.Load())
	}
	// 空闲期间再取到：不回收、不重起
	m2 := New(Options{Idle: 200 * time.Millisecond})
	defer m2.Close()
	c2 := &counter{}
	d2 := c2.def("y", "")
	l, _ = m2.Acquire(context.Background(), d2, wsA)
	l.Release()
	l, _ = m2.Acquire(context.Background(), d2, wsA)
	time.Sleep(400 * time.Millisecond)
	if c2.opens.Load() != 1 || c2.closes.Load() != 0 || len(m2.List()) != 1 {
		t.Fatalf("再取到后不应回收：opens=%d closes=%d", c2.opens.Load(), c2.closes.Load())
	}
	l.Release()
	waitFor(t, "第二次空闲回收", func() bool { return c2.closes.Load() == 1 })
}

func TestImmediateIdle(t *testing.T) {
	m := New(Options{Idle: -1})
	c := &counter{}
	l, _ := m.Acquire(context.Background(), c.def("x", ""), wsA)
	l.Release()
	if len(m.List()) != 0 || c.closes.Load() != 1 {
		t.Fatal("Idle<0 应立即回收")
	}
}

func TestLimit(t *testing.T) {
	m := New(Options{Idle: time.Hour, Max: 2})
	defer m.Close()
	c := &counter{}
	l1, _ := m.Acquire(context.Background(), c.def("1", ""), wsA)
	if _, err := m.Acquire(context.Background(), c.def("2", ""), wsA); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Acquire(context.Background(), c.def("3", ""), wsA); !errors.Is(err, ErrLimit) {
		t.Fatalf("超上限应报 ErrLimit，得 %v", err)
	}
	// 有空闲实例时腾出它
	l1.Release()
	if _, err := m.Acquire(context.Background(), c.def("3", ""), wsA); err != nil {
		t.Fatalf("有空闲实例时应腾出：%v", err)
	}
	if c.closes.Load() != 1 || len(m.List()) != 2 {
		t.Fatalf("closes=%d list=%d", c.closes.Load(), len(m.List()))
	}
}

func TestOpenFailure(t *testing.T) {
	m := New(Options{Idle: time.Hour})
	defer m.Close()
	c := &counter{fail: errors.New("坏了")}
	if _, err := m.Acquire(context.Background(), c.def("x", ""), wsA); err == nil {
		t.Fatal("Open 失败应报错")
	}
	if len(m.List()) != 0 {
		t.Fatal("失败的实例不应留在表里")
	}
	c.fail = nil
	if _, err := m.Acquire(context.Background(), c.def("x", ""), wsA); err != nil {
		t.Fatalf("下次应重新起：%v", err)
	}
}

func TestBoundCloseReleases(t *testing.T) {
	m := New(Options{Idle: 50 * time.Millisecond})
	defer m.Close()
	c := &counter{}
	ba, bb := m.Bind(wsA), m.Bind(wsB)
	g := c.def("g", ScopeGlobal)
	w := c.def("w", ScopeWorkspace)
	for _, b := range []*Bound{ba, bb} {
		if _, err := b.Acquire(context.Background(), g); err != nil {
			t.Fatal(err)
		}
		if _, err := b.Acquire(context.Background(), w); err != nil {
			t.Fatal(err)
		}
	}
	if c.opens.Load() != 3 { // g 一份，w 两份
		t.Fatalf("opens=%d", c.opens.Load())
	}
	ba.Close()
	if _, err := ba.Acquire(context.Background(), g); !errors.Is(err, ErrClosed) {
		t.Fatalf("关了的视图应报 ErrClosed，得 %v", err)
	}
	waitFor(t, "A 的工作区实例回收", func() bool { return c.closes.Load() == 1 })
	ls := m.List()
	if len(ls) != 2 {
		t.Fatalf("应剩全局一份 + B 一份：%+v", ls)
	}
	for _, in := range ls {
		if in.Refs != 1 {
			t.Fatalf("剩下的实例应各有 B 的一个引用：%+v", in)
		}
	}
}

// 并发：同一键只 Open 一次；并发取还后计数归零、全部回收。WSL 里没有 gcc 跑不了 -race，这里只验结果。
func TestConcurrent(t *testing.T) {
	m := New(Options{Idle: -1})
	c := &counter{delay: 20 * time.Millisecond}
	defs := []Def{c.def("g", ScopeGlobal), c.def("w", ScopeWorkspace)}
	var wg sync.WaitGroup
	start := make(chan struct{})
	vals := make([]any, 64)
	hold := make([]*Lease, 64)
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			l, err := m.Acquire(context.Background(), defs[0], wsA)
			if err != nil {
				t.Error(err)
				return
			}
			vals[i], hold[i] = l.Value(), l
			lw, err := m.Acquire(context.Background(), defs[1], Owner{Workspace: fmt.Sprint(i % 4)})
			if err != nil {
				t.Error(err)
				return
			}
			_ = m.List()
			lw.Release()
		}(i)
	}
	close(start)
	wg.Wait()
	for _, v := range vals {
		if v != vals[0] {
			t.Fatalf("同键并发取到了不同实例：%v %v", v, vals[0])
		}
	}
	for _, l := range hold {
		l.Release()
	}
	if n := len(m.List()); n != 0 {
		t.Fatalf("全部交还后应全部回收，剩 %d", n)
	}
	if o, cl := c.opens.Load(), c.closes.Load(); o != cl {
		t.Fatalf("opens=%d closes=%d 应相等", o, cl)
	}
	m.Close()
	if _, err := m.Acquire(context.Background(), defs[0], wsA); !errors.Is(err, ErrClosed) {
		t.Fatalf("关闭后应报 ErrClosed，得 %v", err)
	}
}

// Hold 期间引用归零也不回收（切换工作区的空档）；结束后仍空闲的按空闲时长回收（<0 立即）。
func TestHold(t *testing.T) {
	m := New(Options{Idle: -1})
	defer m.Close()
	c := &counter{}
	d := c.def("g", ScopeGlobal)
	l, _ := m.Acquire(context.Background(), d, wsA)
	done := m.Hold()
	l.Release()
	if len(m.List()) != 1 || c.closes.Load() != 0 {
		t.Fatal("Hold 期间不应回收")
	}
	l, _ = m.Acquire(context.Background(), d, wsB)
	if c.opens.Load() != 1 {
		t.Fatalf("Hold 期间再取应复用：opens=%d", c.opens.Load())
	}
	done()
	done()
	if len(m.List()) != 1 {
		t.Fatal("还有引用时 Hold 结束也不应回收")
	}
	done = m.Hold()
	l.Release()
	done()
	if len(m.List()) != 0 || c.closes.Load() != 1 {
		t.Fatalf("Hold 结束后仍空闲应回收：list=%d closes=%d", len(m.List()), c.closes.Load())
	}
	// 有空闲时长的：Hold 结束后从头计时
	m2 := New(Options{Idle: 50 * time.Millisecond})
	defer m2.Close()
	c2 := &counter{}
	l, _ = m2.Acquire(context.Background(), c2.def("x", ""), wsA)
	done = m2.Hold()
	l.Release()
	time.Sleep(120 * time.Millisecond)
	if len(m2.List()) != 1 {
		t.Fatal("Hold 期间定时器不应回收")
	}
	done()
	waitFor(t, "Hold 结束后按时回收", func() bool { return len(m2.List()) == 0 })
}

func TestManagerClose(t *testing.T) {
	m := New(Options{Idle: time.Hour})
	c := &counter{}
	m.Acquire(context.Background(), c.def("a", ""), wsA)
	m.Acquire(context.Background(), c.def("b", ScopeGlobal), wsA)
	m.Close()
	m.Close()
	if c.closes.Load() != 2 || len(m.List()) != 0 {
		t.Fatalf("Close 应关掉全部：closes=%d", c.closes.Load())
	}
}

func TestEvict(t *testing.T) {
	m := New(Options{Idle: 50 * time.Millisecond})
	defer m.Close()
	c := &counter{}
	d := c.def("e", "")
	l1, _ := m.Acquire(context.Background(), d, wsA)
	l2, _ := m.Acquire(context.Background(), d, wsA)
	if !l1.Evict() {
		t.Fatal("第一次 Evict 应拿掉实例")
	}
	if l2.Evict() || m.Evict(l1.Key()) {
		t.Fatal("已拿掉的实例再 Evict 应返回 false")
	}
	if n := c.closes.Load(); n != 1 {
		t.Fatalf("closes = %d，应 1", n)
	}
	l2.Release() // 空操作
	if len(m.List()) != 0 {
		t.Fatalf("List = %+v", m.List())
	}
	l3, err := m.Acquire(context.Background(), d, wsA)
	if err != nil || c.opens.Load() != 2 || l3.Value() == l1.Value() {
		t.Fatalf("同键再取应新建：opens=%d err=%v", c.opens.Load(), err)
	}
	l3.Release() // 新实例空闲回收照常
	waitFor(t, "新实例空闲回收", func() bool { return c.closes.Load() == 2 })
	time.Sleep(100 * time.Millisecond)
	if n := c.closes.Load(); n != 2 {
		t.Fatalf("closes = %d，应 2", n)
	}
}

func TestEvictIdleStopsTimer(t *testing.T) {
	m := New(Options{Idle: 80 * time.Millisecond})
	defer m.Close()
	c := &counter{}
	l, _ := m.Acquire(context.Background(), c.def("i", ""), wsA)
	k := l.Key()
	l.Release()
	if !m.Evict(k) {
		t.Fatal("空闲实例应能 Evict")
	}
	time.Sleep(150 * time.Millisecond)
	if n := c.closes.Load(); n != 1 {
		t.Fatalf("closes = %d，定时器应已停", n)
	}
	if m.Evict(Key{Kind: "none"}) {
		t.Fatal("不存在的键应返回 false")
	}
}

func TestEvictWhileStarting(t *testing.T) {
	m := New(Options{Idle: time.Hour})
	defer m.Close()
	c := &counter{delay: 100 * time.Millisecond}
	d := c.def("s", "")
	errc := make(chan error, 1)
	go func() { _, err := m.Acquire(context.Background(), d, wsA); errc <- err }()
	waitFor(t, "进入启动", func() bool { return len(m.List()) == 1 })
	k := m.List()[0].Key
	if !m.Evict(k) {
		t.Fatal("启动中应能 Evict")
	}
	if err := <-errc; !errors.Is(err, ErrEvicted) {
		t.Fatalf("err = %v，应 ErrEvicted", err)
	}
	if c.opens.Load() != 1 || c.closes.Load() != 1 || len(m.List()) != 0 {
		t.Fatalf("opens=%d closes=%d list=%d", c.opens.Load(), c.closes.Load(), len(m.List()))
	}
}

func TestEvictConcurrent(t *testing.T) {
	m := New(Options{Idle: -1})
	defer m.Close()
	for round := 0; round < 50; round++ {
		c := &counter{}
		d := c.def(fmt.Sprintf("c%d", round), "")
		var ls []*Lease
		for i := 0; i < 8; i++ {
			l, err := m.Acquire(context.Background(), d, wsA)
			if err != nil {
				t.Fatal(err)
			}
			ls = append(ls, l)
		}
		var wg sync.WaitGroup
		var evicted atomic.Int32
		for i, l := range ls {
			wg.Add(1)
			go func(i int, l *Lease) {
				defer wg.Done()
				if i%2 == 0 {
					if l.Evict() {
						evicted.Add(1)
					}
				} else {
					l.Release()
				}
			}(i, l)
		}
		wg.Wait()
		if n := c.closes.Load(); n != 1 {
			t.Fatalf("round %d closes = %d，应 1", round, n)
		}
		if evicted.Load() > 1 {
			t.Fatalf("round %d 多次 Evict 成功", round)
		}
	}
	if len(m.List()) != 0 {
		t.Fatalf("List = %+v", m.List())
	}
}
