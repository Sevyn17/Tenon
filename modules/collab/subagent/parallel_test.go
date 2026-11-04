package subagent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"ybase/runtime/agent"
	"ybase/runtime/callctx"
	"ybase/runtime/host"
	"ybase/runtime/session"
	"ybase/runtime/store/sqlite"
	"ybase/runtime/toolhub"
)

// slowModel 每次回复前等 d。
type slowModel struct{ d time.Duration }

func (m slowModel) Stream(ctx context.Context, req session.Request, onText func(string)) (session.AssistantItem, session.StopReason, error) {
	time.Sleep(m.d)
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
	h := &host.Host{DB: db, New: func(id, def string) (*agent.Agent, *session.Config, error) {
		hub := toolhub.New(toolhub.Options{Store: agent.ToolStore{DB: db}})
		a := &agent.Agent{Store: db, SessionID: id, Model: slowModel{300 * time.Millisecond}, Hub: hub}
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
	start := time.Now()
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
	if d := time.Since(start); d > 550*time.Millisecond {
		t.Fatalf("两个各 300ms 的委派应并行，用了 %s", d)
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
