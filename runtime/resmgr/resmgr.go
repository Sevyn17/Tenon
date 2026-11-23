// Package resmgr 统一资源管理器（Mortise 客户端改进 M11，docs/50 5.1 决定 5）。
//
// 管理要启动、占内存、有状态的实例（MCP 进程、以后的扩展进程）。实例按 (定义, 范围) 作键：
// 同一个键只起一份，引用计数；引用归零后空闲一段时间再回收（期间再取到就接着用）；
// 实例数有上限；能列出当前在跑的实例。
//
// 范围从大到小：全局、租户（用户）、工作区、会话。定义声明自己最多能共享到哪一级（缺省工作区），
// 取用方（Owner）给出自己所在的租户、工作区、会话，二者合起来就是隔离键。单机时租户恒为 LocalTenant。
//
// 应用宿主（apphost.WorkspaceManager）持有一个 Manager，每打开一个工作区就 Bind 出一个 Bound 注入给组件
// （服务键 ext.ServiceResources）；工作区关掉时 Bound.Close 释放它取过的全部引用。
// 本包是纯 Go：不读文件、不起进程，实例怎么开、怎么关由定义的 Open 决定。
package resmgr

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/Sevyn17/tenon/runtime/ext"
)

// Scope 实例能共享到的范围。
type Scope string

// 范围，从大到小。空串按 ScopeWorkspace。
const (
	ScopeGlobal    Scope = "global"
	ScopeTenant    Scope = "tenant" // 用户（租户）
	ScopeWorkspace Scope = "workspace"
	ScopeSession   Scope = "session"
)

// LocalTenant 单机时的租户。
const LocalTenant = "local"

// DefaultIdle Options.Idle 为 0 时的空闲回收时长。
const DefaultIdle = 5 * time.Minute

// ErrLimit 实例数到了上限，且没有空闲实例可以腾出来。
var ErrLimit = errors.New("resmgr: 实例数已到上限")

// ErrClosed 管理器或绑定视图已关闭。
var ErrClosed = errors.New("resmgr: 已关闭")

// ErrEvicted 实例在启动期间被 Evict 拿掉了。
var ErrEvicted = errors.New("resmgr: 实例已被拿掉")

// KeyBound 组件取本工作区绑定视图的服务键（Requires 里写 KeyBound.Maybe()）。
var KeyBound = ext.NewKey[*Bound](ext.ServiceResources)

// ParseScope 解析配置里的范围名；空串 = 工作区，"user" 是 "tenant" 的别名。
func ParseScope(s string) (Scope, error) {
	switch Scope(s) {
	case "", ScopeWorkspace:
		return ScopeWorkspace, nil
	case ScopeGlobal:
		return ScopeGlobal, nil
	case ScopeTenant, "user":
		return ScopeTenant, nil
	case ScopeSession:
		return ScopeSession, nil
	}
	return "", fmt.Errorf("范围 %q 不合法（global / tenant / workspace / session）", s)
}

// Owner 取用方所在的位置。Tenant 空 = LocalTenant。
type Owner struct {
	Tenant    string
	Workspace string // 工作区键（如规范化的项目目录）
	Session   string
}

// Key 隔离键：同一个键只有一份实例。比范围小的字段为空。
type Key struct {
	Kind      string // 资源类别，如 "mcp"
	ID        string // 定义的标识（含配置指纹：配置变了就是另一份实例）
	Scope     Scope
	Tenant    string
	Workspace string
	Session   string
}

// Def 一种实例的定义。
type Def struct {
	Kind string
	ID   string
	// Name 给人看的名字（List 用）；空 = ID。
	Name string
	// Scope 最多能共享到哪一级；空 = 工作区。
	Scope Scope
	// Idle 本定义的空闲回收时长；0 = 管理器的缺省，<0 = 引用归零立即回收。
	Idle time.Duration
	// Open 起一份实例：返回值（交给取用方）与关闭函数（可空）。ctx 是第一个取用方的。
	Open func(ctx context.Context) (value any, closeFn func(), err error)
}

// key 按定义的范围从 owner 取隔离键。
func (d Def) key(o Owner) (Key, error) {
	sc, err := ParseScope(string(d.Scope))
	if err != nil {
		return Key{}, err
	}
	k := Key{Kind: d.Kind, ID: d.ID, Scope: sc}
	if sc == ScopeGlobal {
		return k, nil
	}
	k.Tenant = o.Tenant
	if k.Tenant == "" {
		k.Tenant = LocalTenant
	}
	switch sc {
	case ScopeWorkspace, ScopeSession:
		if o.Workspace == "" {
			return Key{}, fmt.Errorf("resmgr: %s/%s 按%s分配，但取用方没有工作区", d.Kind, d.ID, sc)
		}
		k.Workspace = o.Workspace
	}
	if sc == ScopeSession {
		if o.Session == "" {
			return Key{}, fmt.Errorf("resmgr: %s/%s 按会话分配，但取用方没有会话", d.Kind, d.ID)
		}
		k.Session = o.Session
	}
	return k, nil
}

// Options 管理器参数。
type Options struct {
	// Idle 引用归零后多久回收；0 = DefaultIdle，<0 = 立即回收。
	Idle time.Duration
	// Max 实例数上限（含正在启动的）；0 = 不限。到上限时先腾出最久没用的空闲实例，没有可腾的报 ErrLimit。
	Max int
}

// Manager 资源管理器。并发安全。
type Manager struct {
	o       Options
	mu      sync.Mutex
	entries map[Key]*entry
	closed  bool
	holds   int // Hold 计数：>0 时不回收
}

type entry struct {
	key   Key
	name  string
	idle  time.Duration
	ready chan struct{} // Open 结束后关闭
	val   any
	close func()
	err   error

	refs      int
	started   time.Time
	idleSince time.Time
	timer     *time.Timer
	gen       int  // 每次进入或离开空闲加一，作废过期的回收定时器
	evicted   bool // 启动期间被 Evict：Open 回来后关掉，取用方拿到 ErrEvicted
}

// New 建管理器。
func New(o Options) *Manager {
	if o.Idle == 0 {
		o.Idle = DefaultIdle
	}
	return &Manager{o: o, entries: map[Key]*entry{}}
}

// Lease 一次取用。用完调 Release（可重复调用）；Value 在 Release 之后不要再用。
type Lease struct {
	m    *Manager
	e    *entry
	once sync.Once
	b    *Bound
}

// Value 实例（Def.Open 返回的值）。
func (l *Lease) Value() any { return l.e.val }

// Key 实例的隔离键。
func (l *Lease) Key() Key { return l.e.key }

// Release 交还引用。引用归零后按空闲时长回收。
func (l *Lease) Release() {
	l.once.Do(func() {
		if l.b != nil {
			l.b.forget(l)
		}
		l.m.release(l.e)
	})
}

// Evict 立即拿掉这份实例（不论还有几个引用）并关闭它，见 Manager.Evict；随后交还本次取用。
// 实例自己结束了（如进程退出）、或取用方明确要关掉它时用。返回实例是否由这次调用拿掉。
func (l *Lease) Evict() bool {
	ok := l.m.evict(l.e)
	l.Release()
	return ok
}

// Evict 立即拿掉键 k 的实例：停掉回收定时器、从表里去掉，在锁外调一次关闭函数。
// 其余持有者之后的 Release 是空操作；同一个键再 Acquire 会起一份新实例。
// 实例还在启动时，Open 回来后立即关闭，这一批取用方拿到 ErrEvicted。没有这个键返回 false。
func (m *Manager) Evict(k Key) bool {
	m.mu.Lock()
	e := m.entries[k]
	m.mu.Unlock()
	if e == nil {
		return false
	}
	return m.evict(e)
}

func (m *Manager) evict(e *entry) bool {
	m.mu.Lock()
	if m.entries[e.key] != e {
		m.mu.Unlock()
		return false
	}
	m.dropLocked(e)
	var closeFn func()
	select {
	case <-e.ready:
		closeFn = e.close
	default:
		e.evicted = true // 还在启动：Open 回来后自己关
	}
	m.mu.Unlock()
	if closeFn != nil {
		closeFn()
	}
	return true
}

// Acquire 按 owner 取 def 的实例：同一个隔离键已有实例就复用（引用加一），没有就起一份。
// 同一个键并发 Acquire 只调一次 Open，其余等它的结果；Open 失败时这一批都拿到同一个错误，下次再取重新起。
func (m *Manager) Acquire(ctx context.Context, def Def, owner Owner) (*Lease, error) {
	if def.Open == nil {
		return nil, fmt.Errorf("resmgr: %s/%s 没有 Open", def.Kind, def.ID)
	}
	k, err := def.key(owner)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrClosed
	}
	if e, ok := m.entries[k]; ok {
		if e.refs == 0 { // 空闲中被再次取到：取消回收
			e.gen++
			if e.timer != nil {
				e.timer.Stop()
				e.timer = nil
			}
			e.idleSince = time.Time{}
		}
		e.refs++
		m.mu.Unlock()
		select {
		case <-e.ready:
		case <-ctx.Done():
			m.release(e)
			return nil, ctx.Err()
		}
		if e.err != nil {
			return nil, e.err
		}
		return &Lease{m: m, e: e}, nil
	}
	var evict *entry
	if m.o.Max > 0 && len(m.entries) >= m.o.Max {
		if evict = m.oldestIdleLocked(); evict == nil {
			m.mu.Unlock()
			return nil, fmt.Errorf("%w（%d）：%s/%s", ErrLimit, m.o.Max, def.Kind, def.ID)
		}
		m.dropLocked(evict)
	}
	idle := def.Idle
	if idle == 0 {
		idle = m.o.Idle
	}
	name := def.Name
	if name == "" {
		name = def.ID
	}
	e := &entry{key: k, name: name, idle: idle, ready: make(chan struct{}), refs: 1, started: time.Now()}
	m.entries[k] = e
	m.mu.Unlock()
	if evict != nil && evict.close != nil {
		evict.close()
	}

	val, closeFn, err := def.Open(ctx)
	m.mu.Lock()
	if err == nil && (m.closed || e.evicted) { // 启动期间管理器关了或实例被拿掉：不留下实例
		err = ErrClosed
		if e.evicted && !m.closed {
			err = ErrEvicted
		}
		if closeFn != nil {
			defer closeFn()
		}
	}
	e.val, e.close, e.err = val, closeFn, err
	if err != nil && m.entries[k] == e {
		delete(m.entries, k)
	}
	close(e.ready)
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return &Lease{m: m, e: e}, nil
}

// oldestIdleLocked 空闲最久的实例；没有返回 nil。调用方持有 m.mu。
func (m *Manager) oldestIdleLocked() *entry {
	var best *entry
	for _, e := range m.entries {
		if e.refs == 0 && (best == nil || e.idleSince.Before(best.idleSince)) {
			best = e
		}
	}
	return best
}

// dropLocked 从表里拿掉 e 并停掉回收定时器（关闭由调用方在锁外做）。调用方持有 m.mu。
func (m *Manager) dropLocked(e *entry) {
	delete(m.entries, e.key)
	e.gen++
	if e.timer != nil {
		e.timer.Stop()
		e.timer = nil
	}
}

func (m *Manager) release(e *entry) {
	m.mu.Lock()
	if m.entries[e.key] != e { // 启动失败已拿掉，或已被腾出 / 关闭
		m.mu.Unlock()
		return
	}
	e.refs--
	if e.refs > 0 {
		m.mu.Unlock()
		return
	}
	e.idleSince = time.Now()
	if !m.closed && m.holds > 0 { // 暂缓回收中：等 Hold 结束再计时
		e.gen++
		e.timer = nil
		m.mu.Unlock()
		return
	}
	if e.idle < 0 || m.closed {
		m.dropLocked(e)
		m.mu.Unlock()
		if e.close != nil {
			e.close()
		}
		return
	}
	m.scheduleLocked(e)
	m.mu.Unlock()
}

// scheduleLocked 给空闲实例 e 起回收定时器。调用方持有 m.mu。
func (m *Manager) scheduleLocked(e *entry) {
	e.gen++
	g := e.gen
	e.timer = time.AfterFunc(e.idle, func() { m.reap(e, g) })
}

// reap 空闲定时器到点：仍然空闲（期间没被再取到）就回收；暂缓回收中则等 Hold 结束重新计时。
func (m *Manager) reap(e *entry, g int) {
	m.mu.Lock()
	if m.entries[e.key] != e || e.refs != 0 || e.gen != g {
		m.mu.Unlock()
		return
	}
	if m.holds > 0 {
		e.timer = nil
		m.mu.Unlock()
		return
	}
	m.dropLocked(e)
	m.mu.Unlock()
	if e.close != nil {
		e.close()
	}
}

// Hold 暂缓回收，直到调用返回的函数（可重复调用，只算一次）。切换工作区时旧运行时先交还引用、
// 新运行时再取：期间引用会短暂归零，Hold 保证空闲时长很短（甚至 <0）时全局实例也不在这个空档里被关掉。
// 结束后仍空闲的实例从头计空闲时长（<0 的立即回收）。
func (m *Manager) Hold() (release func()) {
	m.mu.Lock()
	m.holds++
	m.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			m.holds--
			var closing []func()
			if m.holds == 0 && !m.closed {
				for _, e := range m.entries {
					if e.refs != 0 || e.timer != nil {
						continue
					}
					if e.idle < 0 {
						m.dropLocked(e)
						if e.close != nil {
							closing = append(closing, e.close)
						}
						continue
					}
					m.scheduleLocked(e)
				}
			}
			m.mu.Unlock()
			for _, f := range closing {
				f()
			}
		})
	}
}

// Instance 一个在跑（或正在启动）的实例，给管理页与协议列表用。
type Instance struct {
	Key  Key
	Name string
	// Refs 当前引用数；0 = 空闲，到 IdleSince + 空闲时长时回收。
	Refs      int
	Starting  bool
	Started   time.Time
	IdleSince time.Time // 有引用时为零值
}

// List 当前全部实例，按 Kind、ID、范围、租户、工作区、会话排序。
func (m *Manager) List() []Instance {
	m.mu.Lock()
	out := make([]Instance, 0, len(m.entries))
	for _, e := range m.entries {
		starting := false
		select {
		case <-e.ready:
		default:
			starting = true
		}
		out = append(out, Instance{Key: e.key, Name: e.name, Refs: e.refs, Starting: starting,
			Started: e.started, IdleSince: e.idleSince})
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].Key, out[j].Key
		for _, p := range [][2]string{{a.Kind, b.Kind}, {a.ID, b.ID}, {string(a.Scope), string(b.Scope)},
			{a.Tenant, b.Tenant}, {a.Workspace, b.Workspace}, {a.Session, b.Session}} {
			if p[0] != p[1] {
				return p[0] < p[1]
			}
		}
		return false
	})
	return out
}

// Close 关掉全部实例（进程退出时）；之后 Acquire 返回 ErrClosed。正在启动的实例在启动完成后关闭。可重复调用。
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	var closing []func()
	for _, e := range m.entries {
		select {
		case <-e.ready:
		default:
			continue // 还在启动：Open 回来后见 closed 自己关
		}
		m.dropLocked(e)
		if e.close != nil {
			closing = append(closing, e.close)
		}
	}
	m.mu.Unlock()
	for _, f := range closing {
		f()
	}
}

// Bound 按一个取用方（通常是一个打开的工作区）绑定的视图：Acquire 自动带上 Owner，
// 并记下经它取的全部引用，Close 时一并交还（工作区关掉后它的引用释放，实例按空闲时长回收）。
type Bound struct {
	m      *Manager
	owner  Owner
	mu     sync.Mutex
	leases map[*Lease]struct{}
	closed bool
}

// Bind 绑定取用方。
func (m *Manager) Bind(o Owner) *Bound {
	if o.Tenant == "" {
		o.Tenant = LocalTenant
	}
	return &Bound{m: m, owner: o, leases: map[*Lease]struct{}{}}
}

// Owner 绑定的取用方。
func (b *Bound) Owner() Owner { return b.owner }

// Manager 背后的管理器。
func (b *Bound) Manager() *Manager { return b.m }

// Acquire 同 Manager.Acquire，取用方是绑定的 Owner。
func (b *Bound) Acquire(ctx context.Context, def Def) (*Lease, error) {
	b.mu.Lock()
	closed := b.closed
	b.mu.Unlock()
	if closed {
		return nil, ErrClosed
	}
	l, err := b.m.Acquire(ctx, def, b.owner)
	if err != nil {
		return nil, err
	}
	l.b = b
	b.mu.Lock()
	if b.closed { // 取的同时视图关了：立刻交还
		b.mu.Unlock()
		l.b = nil
		l.Release()
		return nil, ErrClosed
	}
	b.leases[l] = struct{}{}
	b.mu.Unlock()
	return l, nil
}

func (b *Bound) forget(l *Lease) {
	b.mu.Lock()
	delete(b.leases, l)
	b.mu.Unlock()
}

// Close 交还经本视图取的全部引用；之后 Acquire 返回 ErrClosed。可重复调用。
func (b *Bound) Close() {
	b.mu.Lock()
	b.closed = true
	ls := make([]*Lease, 0, len(b.leases))
	for l := range b.leases {
		ls = append(ls, l)
	}
	b.leases = map[*Lease]struct{}{}
	b.mu.Unlock()
	for _, l := range ls {
		l.Release()
	}
}
