package host

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
)

// inboxCounter 数信箱读取次数。
type inboxCounter struct {
	Store
	n atomic.Int32
}

func (c *inboxCounter) Inbox(ctx context.Context, id string) ([]store.InboxItem, error) {
	c.n.Add(1)
	return c.Store.Inbox(ctx, id)
}

// stepModel 前两步各调一次 echo，第三步回文本；记下每次调用时信箱已被读了几次。
type stepModel struct {
	mu   sync.Mutex
	c    *inboxCounter
	seen []int32
}

func (m *stepModel) Stream(_ context.Context, req session.Request, _ func(string)) (session.AssistantItem, session.StopReason, error) {
	m.mu.Lock()
	m.seen = append(m.seen, m.c.n.Load())
	n := len(m.seen)
	m.mu.Unlock()
	if n < 3 {
		return session.AssistantItem{ToolCalls: []session.ToolCall{{Name: "echo", Args: json.RawMessage(`{}`)}}}, session.StopReason{V: "tool_calls"}, nil
	}
	return session.AssistantItem{Content: text("done")}, session.StopReason{V: "stop"}, nil
}

// 回合内没有新信件时，步边界不读信箱（2026-11-04 审查 P1-36：原先每次调模型前都查两次信箱）。
func TestNoInboxReadWithoutNewMail(t *testing.T) {
	m := &stepModel{}
	r := newT2Rig(t, nil, m, 0, nil)
	c := &inboxCounter{Store: r.h.DB}
	m.c = c
	r.h.DB = c
	hd := mustOpen(t, r.h, "s1")
	if err := hd.Do(Command{Kind: Send, Text: "开始"}); err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.seen) != 3 {
		t.Fatalf("应调 3 次模型：%v", m.seen)
	}
	if m.seen[2] != m.seen[1] {
		t.Fatalf("第 2、3 步之间没有新信，不应读信箱：读取计数 %v", m.seen)
	}
}
