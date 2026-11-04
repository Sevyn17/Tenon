package component

// 作用域（plugin-manager §5.3，L1）：context + 清理注册表。
// 四层：进程 → 插件 → 会话 → 回合。父作用域关闭即级联关闭子作用域；
// 清理按注册倒序执行；Go 启动的后台任务受监管（panic 捕获、退避重启、关闭时取消并等待）。

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Scope 一个作用域。并发安全。
type Scope struct {
	name   string
	ctx    context.Context
	cancel context.CancelFunc
	parent *Scope

	mu       sync.Mutex
	closed   bool
	cleanups []func(context.Context) error
	children []*Scope
	wg       sync.WaitGroup
	degraded []string // 重启耗尽的后台任务名

	// 测试可调：退避基准。
	backoff time.Duration
}

// NewRoot 创建进程作用域。
func NewRoot(name string) *Scope {
	ctx, cancel := context.WithCancel(context.Background())
	return &Scope{name: name, ctx: ctx, cancel: cancel, backoff: 200 * time.Millisecond}
}

// Name 作用域名（层级以 / 连接）。
func (s *Scope) Name() string { return s.name }

// Context 作用域的 context；关闭后被取消。
func (s *Scope) Context() context.Context { return s.ctx }

// Child 创建子作用域。父已关闭时返回一个已关闭的子作用域。
func (s *Scope) Child(name string) *Scope {
	ctx, cancel := context.WithCancel(s.ctx)
	c := &Scope{name: s.name + "/" + name, ctx: ctx, cancel: cancel, parent: s, backoff: s.backoff}
	s.mu.Lock()
	closed := s.closed
	if !closed {
		s.children = append(s.children, c)
	}
	s.mu.Unlock()
	if closed {
		c.Close(context.Background())
	}
	return c
}

// OnClose 注册清理函数；作用域已关闭时立即执行。
func (s *Scope) OnClose(f func(context.Context) error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = f(context.Background())
		return
	}
	s.cleanups = append(s.cleanups, f)
	s.mu.Unlock()
}

// MaxRestarts 后台任务失败后的最多重启次数（plugin-manager D3）。
const MaxRestarts = 3

// Go 启动受监管的后台任务：返回错误或 panic 时指数退避重启，最多 MaxRestarts 次，
// 之后记为 degraded（explain 可见）。任务应在 ctx 取消时返回；返回 nil 视为正常结束。
func (s *Scope) Go(name string, f func(ctx context.Context) error) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		delay := s.backoff
		for attempt := 0; ; attempt++ {
			err := runSafe(s.ctx, f)
			if err == nil || s.ctx.Err() != nil {
				return
			}
			if attempt >= MaxRestarts {
				s.mu.Lock()
				s.degraded = append(s.degraded, name)
				s.mu.Unlock()
				return
			}
			select {
			case <-time.After(delay):
			case <-s.ctx.Done():
				return
			}
			delay *= 2
		}
	}()
}

func runSafe(ctx context.Context, f func(context.Context) error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return f(ctx)
}

// Degraded 返回重启耗尽的后台任务名（含子作用域）。
func (s *Scope) Degraded() []string {
	s.mu.Lock()
	out := append([]string(nil), s.degraded...)
	kids := append([]*Scope(nil), s.children...)
	s.mu.Unlock()
	for _, c := range kids {
		out = append(out, c.Degraded()...)
	}
	return out
}

// Close 关闭：先关子作用域（倒序），再取消 context、等待后台任务，最后倒序执行清理。
// 重复调用无副作用。返回全部清理错误的合并。
func (s *Scope) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	kids := s.children
	cleanups := s.cleanups
	s.children, s.cleanups = nil, nil
	s.mu.Unlock()

	var errs []error
	for i := len(kids) - 1; i >= 0; i-- {
		if err := kids[i].Close(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	s.cancel()
	s.wg.Wait()
	for i := len(cleanups) - 1; i >= 0; i-- {
		if err := cleanups[i](ctx); err != nil {
			errs = append(errs, err)
		}
	}
	if s.parent != nil {
		s.parent.detach(s)
	}
	return errors.Join(errs...)
}

func (s *Scope) detach(c *Scope) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, k := range s.children {
		if k == c {
			s.children = append(s.children[:i], s.children[i+1:]...)
			return
		}
	}
}

// Live 返回存活的子作用域数（泄漏测试用）。
func (s *Scope) Live() int {
	s.mu.Lock()
	kids := append([]*Scope(nil), s.children...)
	s.mu.Unlock()
	n := len(kids)
	for _, c := range kids {
		n += c.Live()
	}
	return n
}
