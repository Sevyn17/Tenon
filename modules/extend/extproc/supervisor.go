package extproc

// 进程监管（框架设计 04 4.8，T6b）：崩溃退避重启，连续 MaxCrashes 次后标记不可用并回调；
// 不可用期间调用方按 fail_closed 处理。杀进程用 modules/internal/proc 的杀进程树。

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/Sevyn17/tenon/modules/internal/proc"
)

// 缺省参数。
const (
	DefaultMaxCrashes = 5
	DefaultBackoff    = 200 * time.Millisecond
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
	// OnCrash 进程退出（非主动停止）时回调；可为空。
	OnCrash func(err error)
	// OnUnavailable 连续崩溃超限、标记不可用时回调；可为空。
	OnUnavailable func()

	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	crashes int
	dead    bool
	stopped bool
}

// Start 启动进程并接好 stdin/stdout。
func (s *Supervisor) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startLocked(ctx)
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
	go s.watch(cmd)
	return nil
}

// watch 等进程退出；非主动停止时按退避重启，超限标记不可用。
func (s *Supervisor) watch(cmd *exec.Cmd) {
	err := cmd.Wait()
	s.mu.Lock()
	if s.stopped || s.dead {
		s.mu.Unlock()
		return
	}
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
		s.dead = true
		s.mu.Unlock()
		if s.OnUnavailable != nil {
			s.OnUnavailable()
		}
		return
	}
	time.Sleep(backoff)
	s.mu.Lock()
	if s.stopped || s.dead {
		s.mu.Unlock()
		return
	}
	if e := s.startLocked(context.Background()); e != nil {
		s.mu.Unlock()
		if s.OnUnavailable != nil {
			s.OnUnavailable()
		}
		return
	}
	s.mu.Unlock()
}

// Available 进程可用（没被标记不可用、没停）。
func (s *Supervisor) Available() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.dead && !s.stopped
}

// Crashes 当前连续崩溃次数。
func (s *Supervisor) Crashes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.crashes
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
func (s *Supervisor) Stop() error {
	s.mu.Lock()
	s.stopped = true
	cmd := s.cmd
	s.mu.Unlock()
	if cmd == nil {
		return nil
	}
	return proc.Kill(cmd)
}
