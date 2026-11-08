package tenon

// 多轮与流式（计划/08 3.3）：Session 包住一个会话，Send 接着聊，Stream 逐字输出、报工具调用与确认请求。
// 事件来自协议层发到总线上的同一份流式事件（text_delta、tool_call、tool_result、human_requested），
// SDK 用户和前端看到的是同一种东西。
//
// 确认：Send / Ask 时没有人能回答（没有 WithProtocolUI / WithUI）的确认立即拒绝，不等 confirm_deadline（缺省 24 小时）；
// Stream 把确认交给调用方（Event.Approve），不处理就一直等到截止。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"strings"
	"sync"
	"time"

	"github.com/Sevyn17/tenon/runtime/agentdef"
	"github.com/Sevyn17/tenon/runtime/bus"
	"github.com/Sevyn17/tenon/runtime/host"
	"github.com/Sevyn17/tenon/runtime/mail"
)

// Session 一个会话。并发安全；同一会话同时只跑一轮（后来的 Send 排到前一轮之后）。
type Session struct {
	app   *App
	agent string
	meta  json.RawMessage
	user  string

	mu      sync.Mutex
	id      string
	first   string // 第一封信的信封 id：会话还没建时用（id 由它算出）
	created bool
}

// NewSession 准备一个新会话（第一次 Send / Stream 时才真正建）：agentName 为空时按缺省智能体规则选，
// meta 写进会话的业务 meta。会话 id 现在就确定了（ID()）。
func (a *App) NewSession(ctx context.Context, agentName string, meta Meta) (*Session, error) {
	if err := a.ready(ctx); err != nil {
		return nil, err
	}
	name, err := a.resolveAgent(ctx, agentName)
	if err != nil {
		return nil, err
	}
	var raw json.RawMessage
	if len(meta) > 0 {
		if raw, err = json.Marshal(meta); err != nil {
			return nil, fmt.Errorf("%w: meta: %v", ErrInvalid, err)
		}
	}
	user := userFrom(ctx)
	if user == "" {
		user = LocalUser
	}
	first := mail.NewID()
	return &Session{app: a, agent: name, meta: raw, user: user, first: first,
		id: host.AgentSessionID(name, user, first)}, nil
}

// Session 按 id 找回一个已有会话（如 Reply.SessionID、上次存下的 id）。
func (a *App) Session(ctx context.Context, id string) (*Session, error) {
	if err := a.ready(ctx); err != nil {
		return nil, err
	}
	ok, err := a.handle.DB().Exists(ctx, id)
	if err != nil {
		return nil, wrapErr(err)
	}
	if !ok {
		return nil, fmt.Errorf("%w: 会话 %s", ErrNotFound, id)
	}
	user := userFrom(ctx)
	if user == "" {
		user = LocalUser
	}
	return &Session{app: a, id: id, user: user, created: true}, nil
}

func (a *App) ready(ctx context.Context) error {
	a.mu.Lock()
	started, closed := a.started, a.closed
	a.mu.Unlock()
	if !started || closed {
		return ErrNotStarted
	}
	return checkTenant(ctx)
}

// ID 会话 id。
func (s *Session) ID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.id
}

// Send 发一条消息，等这一轮结束，返回最后一段助手文本。ctx 取消时取消这一轮。
func (s *Session) Send(ctx context.Context, text string) (*Reply, error) {
	return s.turn(ctx, text, nil)
}

// History 读会话的全部条目。
func (s *Session) History(ctx context.Context) ([]*Entry, error) { return s.app.History(ctx, s.ID()) }

// Cancel 取消进行中的一轮（没有进行中的回合时什么也不做）。
func (s *Session) Cancel(ctx context.Context) error {
	if err := s.app.ready(ctx); err != nil {
		return err
	}
	hd, ok := s.app.host.Get(s.ID())
	if !ok {
		return nil
	}
	return wrapErr(hd.Do(host.Command{Kind: host.Cancel}))
}

// EventKind 流式事件的种类。
type EventKind string

const (
	EventText       EventKind = "text"        // 一段正文增量（Text）
	EventReset      EventKind = "reset"       // 模型重试：丢掉这一步已经收到的正文
	EventToolCall   EventKind = "tool_call"   // 发起工具调用（Tool、CallID、Args）
	EventToolResult EventKind = "tool_result" // 工具结果（CallID、Text 是预览、Failed）
	EventConfirm    EventKind = "confirm"     // 工具调用要确认（Tool、CallID、Args、Reason）；用 Approve 回答
	EventDone       EventKind = "done"        // 这一轮结束（Reply）
	EventError      EventKind = "error"       // 出错（Err），之后不再有事件
)

// Event 一个流式事件。
type Event struct {
	Kind   EventKind
	Text   string
	Tool   string
	CallID string
	Args   string
	Reason string
	Failed bool
	Reply  *Reply
	Err    error

	approve func(bool) error
}

// Approve 回答 EventConfirm：true 允许这次调用，false 拒绝。别的事件上调用返回错误。
func (e Event) Approve(ok bool) error {
	if e.approve == nil {
		return fmt.Errorf("%w: 只有 confirm 事件能 Approve", ErrInvalid)
	}
	return e.approve(ok)
}

// Stream 发一条消息，逐个交出这一轮的事件，最后一个是 EventDone 或 EventError。提前跳出循环会取消这一轮。
//
//	for ev := range s.Stream(ctx, "帮我查一下") {
//	    if ev.Kind == tenon.EventText { fmt.Print(ev.Text) }
//	}
func (s *Session) Stream(ctx context.Context, text string) iter.Seq[Event] {
	return func(yield func(Event) bool) {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		stopped := false
		r, err := s.turn(ctx, text, func(ev Event) bool {
			if stopped {
				return false
			}
			if !yield(ev) {
				stopped = true
				cancel()
				return false
			}
			return true
		})
		if stopped {
			return
		}
		if err != nil {
			yield(Event{Kind: EventError, Err: err})
			return
		}
		yield(Event{Kind: EventDone, Text: r.Text, Reply: r})
	}
}

// turn 跑一轮：先订阅这个会话的事件，再投递，等会话空闲。sink 为 nil 时是 Send：确认没人回答就立即拒绝。
func (s *Session) turn(ctx context.Context, text string, sink func(Event) bool) (*Reply, error) {
	a := s.app
	if err := a.ready(ctx); err != nil {
		return nil, err
	}
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("%w: 消息为空", ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	subCtx, stopSub := context.WithCancel(context.WithoutCancel(ctx))
	defer stopSub()
	q := newEventQueue(a.bus.Subscribe(subCtx, bus.Filter{Session: s.id}))

	from := mail.Address{Kind: mail.AddrHuman, ID: s.user}
	if !s.created {
		id, err := a.host.DeliverAgent(ctx, mail.Envelope{ID: s.first, From: from,
			To: mail.Address{Kind: mail.AddrAgent, ID: s.agent}, Kind: mail.KindRequest, Origin: mail.OriginHuman, Body: text}, s.meta)
		if err != nil {
			if errors.Is(err, agentdef.ErrNotFound) {
				return nil, fmt.Errorf("%w: 定义 %s", ErrNotFound, s.agent)
			}
			return nil, wrapErr(err)
		}
		if id != s.id { // 不该发生（id 由同一组输入算出）；以实际为准，只是流式事件可能漏掉开头
			s.id = id
		}
		s.created = true
	} else if err := a.host.Deliver(ctx, mail.Envelope{From: from, To: mail.Address{Kind: mail.AddrSession, ID: s.id},
		Kind: mail.KindRequest, Origin: mail.OriginHuman, Body: text}); err != nil {
		return nil, wrapErr(err)
	}
	hd, ok := a.host.Get(s.id)
	if !ok {
		var err error
		if hd, err = a.host.Open(ctx, s.id); err != nil {
			return nil, wrapErr(err)
		}
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- hd.Wait() }()

	// 没人回答的确认（Send，且没有 WithProtocolUI / WithUI）：立即拒绝。
	autoDeny := sink == nil && !a.protocolUIOn
	ended := false // 这一轮的 turn_end（或 turn_denied）到了
	handle := func(e bus.Envelope) {
		if e.Kind == "turn_end" || e.Kind == "turn_denied" {
			ended = true
		}
		ev, ok := translate(e)
		if !ok {
			return
		}
		if ev.Kind == EventConfirm {
			callID := ev.CallID
			if autoDeny {
				go func() { _ = hd.Do(host.Command{Kind: host.Confirm, CallID: callID, Approve: false}) }()
				return
			}
			ev.approve = func(ok bool) error {
				return wrapErr(hd.Do(host.Command{Kind: host.Confirm, CallID: callID, Approve: ok}))
			}
		}
		if sink != nil {
			sink(ev)
		}
	}
	for {
		select {
		case e := <-q.out:
			handle(e)
		case err := <-waitCh:
			// 回合里发出的事件这时已经在订阅缓冲里（宿主先发 turn_end 再处理 Wait 的屏障），
			// 但转交的协程可能还没送到：一直交到这一轮的 turn_end 为止，兜底等 2 秒。
			if err == nil && !ended {
				limit := time.NewTimer(2 * time.Second)
			drain:
				for !ended {
					select {
					case e := <-q.out:
						handle(e)
					case <-limit.C:
						break drain
					}
				}
				limit.Stop()
			}
			if err != nil {
				return nil, wrapErr(err)
			}
			out, err := lastAssistant(ctx, a.handle, s.id)
			if err != nil {
				return nil, wrapErr(err)
			}
			return &Reply{Text: out, SessionID: s.id}, nil
		case <-ctx.Done():
			_ = hd.Post(host.Command{Kind: host.Cancel})
			return nil, ctx.Err()
		}
	}
}

// translate 总线上的流式事件 → Event；不关心的种类返回 false。
func translate(e bus.Envelope) (Event, bool) {
	var d struct {
		Text    string `json:"text"`
		CallID  string `json:"call_id"`
		Name    string `json:"name"`
		Tool    string `json:"tool"`
		Args    string `json:"args"`
		Preview string `json:"preview"`
		Error   bool   `json:"error"`
		Reason  string `json:"reason"`
	}
	if e.Data != nil {
		b, _ := json.Marshal(e.Data)
		_ = json.Unmarshal(b, &d)
	}
	switch e.Kind {
	case "text_delta":
		return Event{Kind: EventText, Text: d.Text}, d.Text != ""
	case "text_reset":
		return Event{Kind: EventReset}, true
	case "tool_call":
		return Event{Kind: EventToolCall, Tool: d.Name, CallID: d.CallID, Args: d.Args}, true
	case "tool_result":
		return Event{Kind: EventToolResult, CallID: d.CallID, Text: d.Preview, Failed: d.Error}, true
	case "human_requested":
		return Event{Kind: EventConfirm, Tool: d.Tool, CallID: d.CallID, Args: d.Args, Reason: d.Reason}, true
	}
	return Event{}, false
}

// eventQueue 把总线订阅及时读空（总线会断开读得慢的订阅者），交给调用方慢慢处理。
type eventQueue struct {
	out chan bus.Envelope
}

func newEventQueue(sub *bus.Sub) *eventQueue {
	q := &eventQueue{out: make(chan bus.Envelope)}
	var (
		mu   sync.Mutex
		buf  []bus.Envelope
		wake = make(chan struct{}, 1)
		done = make(chan struct{})
	)
	go func() { // 读总线：只追加，不阻塞
		defer close(done)
		for e := range sub.C() {
			mu.Lock()
			buf = append(buf, e)
			mu.Unlock()
			select {
			case wake <- struct{}{}:
			default:
			}
		}
	}()
	go func() { // 交给调用方：按顺序一个一个给
		for {
			mu.Lock()
			if len(buf) == 0 {
				mu.Unlock()
				select {
				case <-wake:
					continue
				case <-done:
					mu.Lock()
					left := len(buf)
					mu.Unlock()
					if left == 0 {
						return
					}
					continue
				}
			}
			e := buf[0]
			buf = buf[1:]
			mu.Unlock()
			select {
			case q.out <- e:
			case <-done:
				return
			}
		}
	}()
	return q
}
