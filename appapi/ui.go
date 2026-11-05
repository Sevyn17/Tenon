package appapi

// UI 服务（runtime.UI）的事件化实现：Print / Warn 发 notice，Ask 向人提问并等回答。供桌面端 / api-server 用；
// CLI 有自己的终端界面。UI 在装配时就要注入插件（早于 App 构造），所以只依赖总线，由应用先构造再交给 Options.UI。
//
// 交互（06 10.6）：App.New 把宿主交给 UI 之后，会话已打开时的提问登记成 question 交互（Host.Ask），
// 客户端经 interaction.respond 回答。没有宿主或会话没打开时（装配期提问）退回进程内的等待通道，
// 只发内部事件 ask（不上线），由 Answer 送回答案。

import (
	"context"
	"sync"

	"ybase/runtime/bus"
	"ybase/runtime/host"
	"ybase/runtime/store"
)

// UI 事件化界面。零值不可用，用 NewUI 构造。
type UI struct {
	b       *bus.Bus
	mu      sync.Mutex
	pending map[string]chan string
	host    *host.Host // App.New 设置：会话里的提问登记成 question 交互；nil = 只用进程内等待
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
// 会话在本进程打开着时登记成 question 交互（protocol 2，06 10.6：持久、出现在快照里、可经 interaction.respond 回答），
// ask_id 就是交互 id；没有会话（装配期提问）时退回进程内等待。
func (u *UI) AskIn(ctx context.Context, sessionID, question string, options []string) (string, error) {
	if options == nil {
		options = []string{}
	}
	u.mu.Lock()
	h := u.host
	u.mu.Unlock()
	if h != nil && sessionID != "" {
		if _, ok := h.Get(sessionID); ok {
			return h.Ask(ctx, sessionID, question, options, func(iid string) {
				u.publishAsk(sessionID, iid, question, options)
			})
		}
	}
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
	u.publishAsk(sessionID, id, question, options)
	select {
	case a := <-ch:
		return a, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (u *UI) publishAsk(sessionID, id, question string, options []string) {
	u.b.Publish(bus.Envelope{Session: sessionID, Kind: EventAsk, Data: map[string]any{"ask_id": id, "question": question, "options": options, "session": sessionID}})
}

// Answer 送回 ask_id 的答案（进程内调用）。交互化的提问按交互结束（只收第一条）；
// 没有该问题（已超时 / 已回答）返回 not_found。
func (u *UI) Answer(askID, text string) error {
	u.mu.Lock()
	ch, ok := u.pending[askID]
	delete(u.pending, askID)
	h := u.host
	u.mu.Unlock()
	if ok {
		ch <- text
		return nil
	}
	if h != nil {
		_, won, err := h.Respond(context.Background(), askID, store.Resolution{Outcome: host.OutcomeAnswered,
			By: LocalPrincipal.ID, Answer: text}, host.Answer{Text: text})
		if err == nil && won {
			return nil
		}
	}
	return Errorf(CodeNotFound, "没有待回答的问题 %s", askID)
}

// bindHost App.New 调用：之后的提问登记成交互。
func (u *UI) bindHost(h *host.Host) {
	u.mu.Lock()
	u.host = h
	u.mu.Unlock()
}
