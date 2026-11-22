package extproc

// 进程监管（框架设计 04 4.8，T6b）：崩溃退避重启，连续 MaxCrashes 次后标记不可用并回调；
// 不可用期间调用方按 fail_closed 处理。杀进程用 pkg/proc 的杀进程树。

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/Sevyn17/tenon/pkg/proc"
)

// 缺省参数。
const (
	DefaultMaxCrashes = 5
	DefaultBackoff    = 200 * time.Millisecond
	// DefaultStableFor进程稳定运行多久后清零连续崩溃计数。
	DefaultStableFor = time.Minute
)

// Supervisor 监管一个外部插件进程。
type Supervisor struct {
	Argv []string // 启动命令
	Dir  string   // 工作目录（可空）
	Env  []string // 额外环境变量（可空）
	// MaxCrashes 连续崩溃多少次后标记不可用（<=0 用 DefaultMaxCrashes）。
	MaxCrashes int
	// Backoff 首次退避（<=0 用 DefaultBackoff）；每次崩溃翻倍，封顶 5 秒。
	Backoff time.Duration
	// StableFor进程稳定运行多久后清零连续崩溃计数（<=0用 DefaultStableFor）。
	StableFor time.Duration
	// OnCrash 进程退出（非主动停止）时回调；可为空。
	OnCrash func(err error)
	// OnRestart进程重启成功后回调；可为空。组件可在这里重建回桥并重新握手。
	//返回错误表示重启后的初始化失败，Supervisor会标记不可用并终止新进程。
	OnRestart func() error
	// OnUnavailable 连续崩溃超限、标记不可用时回调；可为空。
	OnUnavailable func()

	// restartMu串行化崩溃处理与重启回调，避免新进程很快再次退出时并发重建连接。
	restartMu sync.Mutex
	mu        sync.Mutex
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	crashes   int
	running   bool
	dead      bool
	stopped   bool
}

// Start 启动进程并接好 stdin/stdout。
func (s *Supervisor) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.startLocked(ctx); err != nil {
		s.failLocked()
		return err
	}
	return nil
}

func (s *Supervisor) startLocked(ctx context.Context) error {
	if len(s.Argv) == 0 {
		return fmt.Errorf("extproc: 没给启动命令")
	}
	cmd := exec.CommandContext(ctx, s.Argv[0], s.Argv[1:]...)
	cmd.Dir = s.Dir
	if len(s.Env) > 0 {
		cmd.Env = append(cmd.Environ(), s.Env...)
	}
	proc.SetTree(cmd)
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("extproc: 启动插件进程：%w", err)
	}
	s.cmd, s.stdin, s.stdout = cmd, in, out
	s.running = true
	go s.watch(cmd)
	stableFor := s.StableFor
	if stableFor <= 0 {
		stableFor = DefaultStableFor
	}
	go s.resetCrashesAfterStable(cmd, stableFor)
	return nil
}

// resetCrashesAfterStable只清当前仍存活进程的计数；旧进程的定时器不能清掉新崩溃。
func (s *Supervisor) resetCrashesAfterStable(cmd *exec.Cmd, stableFor time.Duration) {
	timer := time.NewTimer(stableFor)
	defer timer.Stop()
	<-timer.C
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd == cmd && s.running && !s.dead && !s.stopped {
		s.crashes = 0
	}
}

// failLocked 启动失败：标记不可用并回调（调用方不能拿着 nil conn 继续用）。调用方持锁。
func (s *Supervisor) failLocked() {
	s.dead = true
	if s.OnUnavailable != nil {
		go s.OnUnavailable() // 回调里可能再取锁，放 goroutine 里避免死锁
	}
}

// watch 等进程退出；非主动停止时按退避重启，超限标记不可用。
func (s *Supervisor) watch(cmd *exec.Cmd) {
	err := cmd.Wait()
	// Wait已返回就先标记退出，避免稳定期定时器把等待重启锁的死进程当成存活。
	s.mu.Lock()
	if s.cmd == cmd {
		s.running = false
	}
	s.mu.Unlock()
	s.restartMu.Lock()
	defer s.restartMu.Unlock()

	s.mu.Lock()
	if s.cmd != cmd || s.stopped || s.dead {
		s.mu.Unlock()
		return
	}
	s.running = false
	s.crashes++
	n := s.crashes
	limit := s.MaxCrashes
	if limit <= 0 {
		limit = DefaultMaxCrashes
	}
	backoff := s.Backoff
	if backoff <= 0 {
		backoff = DefaultBackoff
	}
	for i := 1; i < n && backoff < 5*time.Second; i++ {
		backoff *= 2
	}
	if backoff > 5*time.Second {
		backoff = 5 * time.Second
	}
	s.mu.Unlock()

	if s.OnCrash != nil {
		s.OnCrash(err)
	}
	if n >= limit {
		s.mu.Lock()
		if !s.stopped && !s.dead {
			s.failLocked()
		}
		s.mu.Unlock()
		return
	}
	time.Sleep(backoff)
	s.mu.Lock()
	if s.stopped || s.dead {
		s.mu.Unlock()
		return
	}
	if e := s.startLocked(context.Background()); e != nil {
		s.failLocked()
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	//管道只能在组件层转成 Conn；回调期间不能持 s.mu（它要读取新管道）。
	if s.OnRestart != nil {
		if e := s.OnRestart(); e != nil {
			s.mu.Lock()
			if s.stopped || s.dead {
				s.mu.Unlock()
				return
			}
			s.running = false
			s.failLocked()
			cmd = s.cmd
			s.mu.Unlock()
			_ = proc.Kill(cmd)
			return
		}
	}
}

// Available 进程可用（没被标记不可用、没停）。
func (s *Supervisor) Available() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.dead && !s.stopped
}

// Stdin 给回桥用。
func (s *Supervisor) Stdin() io.WriteCloser {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stdin
}

// Stdout 给回桥用。
func (s *Supervisor) Stdout() io.ReadCloser {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stdout
}

// Stop 主动停止：杀进程树，不再重启。
//
// 幂等：「已经停了」不算失败。进程可能在我们动手之前就自己崩了（这正是监管对象），
// 此时 proc.Kill 对已退出的进程返回 nil（见 pkg/proc 的 killTree 契约），
// 所以这里直接返回它的结果即可；重复调用 Stop 也安全。
func (s *Supervisor) Stop() error {
	s.mu.Lock()
	s.stopped = true
	s.running = false
	cmd := s.cmd
	s.mu.Unlock()
	if cmd == nil {
		return nil
	}
	return proc.Kill(cmd)
}
