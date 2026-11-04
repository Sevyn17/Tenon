package appapi

// UI 服务（runtime.UI）的事件化实现：Print / Warn 发 notice，Ask 发 ask{ask_id, question, options}
// 并等 session.answer 送回答案。供桌面端 / api-server 用；CLI 有自己的终端界面。
// UI 在装配时就要注入插件（早于 App 构造），所以只依赖总线，由应用先构造再交给 Options.UI。

import (
	"context"
	"sync"

	"ybase/runtime/bus"
	"ybase/runtime/host"
)

// UI 事件化界面。零值不可用，用 NewUI 构造。
type UI struct {
	b       *bus.Bus
	mu      sync.Mutex
	pending map[string]chan string
}

// NewUI 构造发到总线 b 的界面。
func NewUI(b *bus.Bus) *UI { return &UI{b: b, pending: map[string]chan string{}} }

// Print 插件输出 → notice。
func (u *UI) Print(text string) { u.PrintIn("", text) }

// PrintIn 带会话的输出（assemble.SessionUI）：事件带 session，界面按会话路由。
func (u *UI) PrintIn(sessionID, text string) {
	u.b.Publish(bus.Envelope{Session: sessionID, Kind: EventNotice, Data: map[string]any{"text": text}})
}

// Warn 插件告警 → notice（带来源前缀）。
func (u *UI) Warn(source, msg string) {
	u.b.Publish(bus.Envelope{Kind: EventNotice, Data: map[string]any{"text": "[" + source + "] " + msg}})
}

// Ask 发 ask 事件并等回答（ctx 结束返回 ctx.Err()）。
func (u *UI) Ask(ctx context.Context, question string, options []string) (string, error) {
	return u.AskIn(ctx, "", question, options)
}

// AskIn 带会话的提问（assemble.SessionUI，审查 P1-28）：ask 事件带 session，多会话界面知道是哪个会话在问。
func (u *UI) AskIn(ctx context.Context, sessionID, question string, options []string) (string, error) {
	id := host.NewItemID()
	ch := make(chan string, 1)
	u.mu.Lock()
	u.pending[id] = ch
	u.mu.Unlock()
	defer func() {
		u.mu.Lock()
		delete(u.pending, id)
		u.mu.Unlock()
	}()
	if options == nil {
		options = []string{}
	}
	u.b.Publish(bus.Envelope{Session: sessionID, Kind: EventAsk, Data: map[string]any{"ask_id": id, "question": question, "options": options, "session": sessionID}})
	select {
	case a := <-ch:
		return a, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// Answer 送回 ask_id 的答案；没有该问题（已超时 / 已回答）返回 not_found。
func (u *UI) Answer(askID, text string) error {
	u.mu.Lock()
	ch, ok := u.pending[askID]
	delete(u.pending, askID)
	u.mu.Unlock()
	if !ok {
		return Errorf(CodeNotFound, "没有待回答的问题 %s", askID)
	}
	ch <- text
	return nil
}
