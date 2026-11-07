package subagent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"tenon/runtime/agent"
	"tenon/runtime/callctx"
	"tenon/runtime/host"
	"tenon/runtime/session"
	"tenon/runtime/store/sqlite"
	"tenon/runtime/toolhub"
)

// overlapModel 记录同时在跑的调用数；第一次调用进来到第二个也进来（或等够 overlapWait）才一起返回。
// 并行与否看的是"两个调用有没有同时在飞"，不是墙钟耗时——负载高时 300ms 的睡眠会被拉到 500ms 以上，
// 按耗时断言会偶发失败。
type overlapModel struct {
	inFlight atomic.Int32
	peak     atomic.Int32
	mu       sync.Mutex
	release  chan struct{}
	once     sync.Once
}

// overlapWait 等第二个调用进来的上限：串行时第一个调用等这么久后放行，断言再报失败。
const overlapWait = 3 * time.Second

func (m *overlapModel) Stream(ctx context.Context, req session.Request, onText func(string)) (session.AssistantItem, session.StopReason, error) {
	n := m.inFlight.Add(1)
	defer m.inFlight.Add(-1)
	for {
		p := m.peak.Load()
		if n <= p || m.peak.CompareAndSwap(p, n) {
			break
		}
	}
	if n >= 2 {
		m.once.Do(func() { close(m.release) })
	} else {
		select {
		case <-m.release:
		case <-time.After(overlapWait):
		case <-ctx.Done():
		}
	}
	return session.AssistantItem{Content: session.Content{session.NewTextPart("done")}}, session.StopReason{V: "stop"}, nil
}

// 一批里的两个委派并行跑（2026-11-04 审查 P2 工具体验：原先 Parallel=false，串行）。
func TestSubagentCallsRunInParallel(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "s.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	m := &overlapModel{release: make(chan struct{})}
	h := &host.Host{DB: db, New: func(id, def string) (*agent.Agent, *session.Config, error) {
		hub := toolhub.New(toolhub.Options{Store: agent.ToolStore{DB: db}})
		a := &agent.Agent{Store: db, SessionID: id, Model: m, Hub: hub}
		return a, &session.Config{Model: "m", Policy: session.DefaultPolicy()}, nil
	}}
	hub := toolhub.New(toolhub.Options{Store: agent.ToolStore{DB: db}})
	if err := hub.Register(Tool(h, h.New)); err != nil {
		t.Fatal(err)
	}
	calls := []session.ToolCall{
		{ID: "call-aaaaaaaa", Name: "subagent", Args: json.RawMessage(`{"task":"一"}`)},
		{ID: "call-bbbbbbbb", Name: "subagent", Args: json.RawMessage(`{"task":"二"}`)},
	}
	run := hub.Execute(ctx, callctx.New(callctx.Options{SessionID: "p"}), calls, nil)
	for {
		res, ok := run.Next()
		if !ok {
			break
		}
		if res.Err != nil || res.Output.Status != session.ToolOK {
			t.Fatalf("委派失败：%+v %v", res.Output, res.Err)
		}
	}
	if p := m.peak.Load(); p < 2 {
		t.Fatalf("两个委派应同时在跑，最多只有 %d 个（串行）", p)
	}
}

// 同一子会话 id 的调用串行，不同 id 互不等待。
func TestChildLocks(t *testing.T) {
	var l childLocks
	var inside atomic.Int32
	done := make(chan struct{})
	unlock := l.lock("a")
	go func() {
		u := l.lock("a")
		inside.Add(1)
		u()
		close(done)
	}()
	ub := l.lock("b") // 不同 id 不受 a 阻塞
	ub()
	time.Sleep(50 * time.Millisecond)
	if inside.Load() != 0 {
		t.Fatal("同一 id 的第二次调用应等第一次结束")
	}
	unlock()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("第一次结束后第二次应拿到锁")
	}
	if len(l.m) != 0 {
		t.Fatalf("用完应清掉：%v", l.m)
	}
}
