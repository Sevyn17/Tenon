package appapi

// 连接（protocol 2，docs/01 - 设计文档/06 - 应用协议契约.md 10.2、10.5、10.8）：一个客户端连接的身份、订阅与出站队列。
// 传输层（WebSocket、Wails）每个连接建一个 Conn：命令经 Conn.Call 执行（按角色过滤），事件经 Conn.Next 取出发送。
//
// 订阅的事件来源：
//   - 持久事件一律从事件表读（按游标升序，不漏不重）。本进程提交时总线上的提交通知只是"该去读了"的信号：
//     游标正好接上就直接用，否则按游标补读；另外按连接的 poll 间隔轮询一次，补上别的进程写的事件（10.2.4）。
//   - 易失事件来自总线，和提交通知在同一个协程里按发布顺序处理，所以本进程内两类事件的相对顺序不变。
//
// 只推 10.5 两张表里的公开事件，只推连接订阅了的会话（children 时含子会话）。
//
// 背压（10.2.3）：出站队列超过 softQueue 时合并同一 stream_id 的 message.delta、同一 call_id 的 tool.args_delta，
// 丢弃 tool.progress 与 notice；仍超过 hardQueue 时断开积压最多的那个订阅，发 session.lagged {cursor}，
// cursor 是那个会话最后一条确实交给传输层的持久事件。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/runtime/bus"
	"github.com/Sevyn17/tenon/runtime/host"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
)

// pollInterval 跨进程轮询间隔（只在有订阅时轮询）。
const pollInterval = 500 * time.Millisecond

const (
	softQueue = 256  // 出站队列超过它开始合并与丢弃易失事件
	hardQueue = 1024 // 合并后仍超过它就断开积压最多的订阅
)

// viewerCommands viewer 可以执行的命令：只查询和订阅（10.8）。
var viewerCommands = map[string]bool{
	"hello": true, "sessions.list": true, "session.get": true, "session.entries": true, "session.stats": true,
	"session.subscribe": true, "session.unsubscribe": true, "mode.list": true, "mode.get": true, "mode.show": true,
	"artifact.get": true, "trace.explain": true, "interactions.list": true, "plugins.list": true, "packages.list": true, "workspace.files": true, "workspace.list": true, "models.list": true, "feedback.list": true, "session.followups": true,
}

// Conn 一个客户端连接。方法并发安全；用完调用 Close。
type Conn struct {
	a      *App
	p      wire.Principal
	ctx    context.Context
	cancel context.CancelFunc

	mu         sync.Mutex
	subs       map[string]*subscription
	queue      []wire.Envelope
	delivered  map[string]int64 // 会话 → 已交给传输层的最后一个持久游标（lagged 用）
	wake       chan struct{}    // 队列有新事件（容量 1）
	space      chan struct{}    // 队列被取走时关闭并换新（回放时等空间用）
	closed     bool
	poll       time.Duration // 跨进程轮询间隔；0 = pollInterval
	soft, hard int           // 背压阈值；0 = softQueue / hardQueue
}

type subscription struct {
	cancel context.CancelFunc
}

// NewConn 为一个连接建订阅状态；p 是传输层鉴权得出的身份。ctx 结束等同 Close。
func (a *App) NewConn(ctx context.Context, p wire.Principal) *Conn {
	cctx, cancel := context.WithCancel(ctx)
	c := &Conn{a: a, p: p, ctx: cctx, cancel: cancel, subs: map[string]*subscription{}, delivered: map[string]int64{},
		wake: make(chan struct{}, 1), space: make(chan struct{})}
	// 连接级订阅在返回前挂上：连接建好之后的变化一条不漏（原先在协程里订阅，刚建连接时的事件可能丢）。
	go c.watchSessions(a.o.Bus.Subscribe(cctx, bus.Filter{Kinds: []string{store.EventRunState, store.EventMeta, EventDeleted}}))
	context.AfterFunc(cctx, c.Close)
	return c
}

// Principal 连接身份。
func (c *Conn) Principal() wire.Principal { return c.p }

// pollEvery 这个连接的跨进程轮询间隔。
func (c *Conn) pollEvery() time.Duration {
	if c.poll > 0 {
		return c.poll
	}
	return pollInterval
}

// limits 这个连接的背压阈值。
func (c *Conn) limits() (int, int) {
	soft, hard := c.soft, c.hard
	if soft == 0 {
		soft = softQueue
	}
	if hard == 0 {
		hard = hardQueue
	}
	return soft, hard
}

// Close 结束全部订阅；之后 Next 返回错误。可重复调用。
func (c *Conn) Close() {
	c.cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	for id, s := range c.subs {
		s.cancel()
		delete(c.subs, id)
	}
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// Call 执行一条 protocol 2 命令：订阅命令在这里处理，其余交给 App.Call；viewer 只能查询和订阅（forbidden）。
func (c *Conn) Call(ctx context.Context, cmd string, args json.RawMessage) (any, error) {
	if c.p.Role != wire.RoleOperator && !viewerCommands[cmd] {
		return nil, Errorf(wire.CodeForbidden, "身份 %s（%s）不能执行 %s", c.p.ID, c.p.Role, cmd)
	}
	switch cmd {
	case "session.subscribe":
		return bind(func(_ *App, ctx context.Context, in wire.SubscribeArgs) (wire.Empty, error) {
			return wire.Empty{}, c.Subscribe(ctx, in)
		})(ctx, c.a, args)
	case "feedback.set": // 记下是谁打的分
		return bind(func(v *App, ctx context.Context, in wire.FeedbackArgs) (wire.Feedback, error) {
			return v.FeedbackSet(ctx, in, c.p.ID)
		})(ctx, c.a, args)
	case "interaction.respond": // 回答者是连接身份（10.6）
		return bind(func(v *App, ctx context.Context, in wire.RespondArgs) (wire.RespondResult, error) {
			return v.respondAs(ctx, in, c.p)
		})(ctx, c.a, args)
	case "interactions.list":
		return bind(func(v *App, ctx context.Context, _ wire.Empty) (wire.InteractionsResult, error) {
			return v.interactionsFor(ctx, c.p)
		})(ctx, c.a, args)
	case "session.unsubscribe":
		return bind(func(_ *App, _ context.Context, in wire.SessionArgs) (wire.Empty, error) {
			c.Unsubscribe(in.Session)
			return wire.Empty{}, nil
		})(ctx, c.a, args)
	}
	out, err := c.a.Call(ctx, cmd, args)
	if r, ok := out.(wire.HelloResult); ok && err == nil {
		r.Principal = c.p
		out = r
	}
	return out, err
}

// Subscribe 订阅会话：回放 cursor > after 的持久事件 → session.synced {cursor} → 实时（持久 + 易失）。
// after 缺省为当前游标（只收新事件）。同一会话重复订阅以新的为准。游标早于保留期返回 cursor_expired。
func (c *Conn) Subscribe(ctx context.Context, in wire.SubscribeArgs) error {
	if err := need(in.Session); err != nil {
		return err
	}
	db := c.a.o.DB
	var after int64
	if in.After != nil {
		after = *in.After
	} else {
		l, err := host.LoadHead(ctx, db, in.Session)
		if err != nil {
			return err
		}
		after = l.Cursor
	}
	sctx, cancel := context.WithCancel(c.ctx)
	// 先挂上总线订阅再读事件表：两次之间提交的事件靠游标去重，不会漏。
	live := c.a.o.Bus.Subscribe(sctx, bus.Filter{Session: in.Session, Tree: in.Children})
	evs, wm, err := db.Replay(ctx, in.Session, after)
	if err != nil {
		cancel()
		return err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		cancel()
		return fmt.Errorf("%w: 连接已关闭", host.ErrClosed)
	}
	if old := c.subs[in.Session]; old != nil {
		old.cancel()
	}
	c.subs[in.Session] = &subscription{cancel: cancel}
	c.delivered[in.Session] = after
	c.mu.Unlock()
	go c.run(sctx, in.Session, live, evs, max(wm, after))
	return nil
}

// Unsubscribe 取消订阅，丢掉队列里这个会话还没发出的事件。
func (c *Conn) Unsubscribe(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s := c.subs[id]; s != nil {
		s.cancel()
		delete(c.subs, id)
		c.dropLocked(id)
	}
}

// run 一个订阅的主循环（一个协程：持久与易失按到达顺序处理）。
func (c *Conn) run(ctx context.Context, id string, live *bus.Sub, replay []bus.Envelope, last int64) {
	defer live.Close()
	for _, e := range replay {
		if !c.pushWait(ctx, c.translate(e)) {
			return
		}
	}
	if len(replay) > 0 {
		last = max(last, replay[len(replay)-1].Seq)
	}
	if !c.pushWait(ctx, []wire.Envelope{c.envelope(id, wire.EvSynced, 0, 0, wire.CursorRef{Cursor: last})}) {
		return
	}
	catchUp := func() bool {
		evs, _, err := c.a.o.DB.Replay(ctx, id, last)
		if err != nil {
			if errors.Is(err, store.ErrCursorExpired) || errors.Is(err, store.ErrNotFound) {
				c.lag(id)
				return false
			}
			return ctx.Err() == nil // 暂时读不到：下次轮询再试
		}
		for _, e := range evs {
			if e.Seq <= last {
				continue
			}
			last = e.Seq
			c.push(ctx, c.translate(e))
		}
		return true
	}
	tick := time.NewTicker(c.pollEvery())
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if !catchUp() {
				return
			}
		case e, ok := <-live.C():
			if !ok {
				if ctx.Err() == nil { // 总线判定过慢断开（或总线关闭）：按 lagged 交还游标
					c.lag(id)
				}
				return
			}
			switch {
			case e.Session == id && e.Kind == EventDeleted: // 会话已删除：发 session.closed 并结束订阅（不再回放、不发 lagged）
				c.push(ctx, []wire.Envelope{c.envelope(id, wire.EvSessionClosed, 0, 0, wire.SessionClosed{Reason: "deleted"})})
				c.mu.Lock()
				if sub := c.subs[id]; sub != nil {
					sub.cancel()
					delete(c.subs, id)
				}
				c.mu.Unlock()
				return
			case e.Session == id && e.Seq > 0: // 本会话的提交通知
				if e.Seq <= last {
					continue
				}
				if e.Seq == last+1 {
					last = e.Seq
					c.push(ctx, c.translate(e))
				} else if !catchUp() {
					return
				}
			default: // 易失事件，或 children 下子会话的事件（游标属于子会话自己的序列）
				c.push(ctx, c.translate(e))
			}
		}
	}
}

// translate 总线事件 → 公开的线上事件（0–2 条）。不在 10.5 两张表里的一律不推。
func (c *Conn) translate(e bus.Envelope) []wire.Envelope {
	if v, ok := e.Data.(v2Event); ok { // stream2.go 发的分段流式事件：载荷已是线上类型
		if !wire.Public(e.Kind) {
			return nil
		}
		return []wire.Envelope{c.envelope(e.Session, e.Kind, 0, v.Turn, v.Payload)}
	}
	m := dataMap(e.Data)
	switch e.Kind {
	case EventTurnStart:
		turn := uint32(intOf(m["turn"]))
		inputs := stringList(m["inputs"])
		if inputs == nil {
			inputs = []string{}
		}
		return []wire.Envelope{c.envelope(e.Session, wire.EvTurnStarted, 0, turn, wire.TurnStarted{Turn: turn, Inputs: inputs})}
	case store.EventEntry:
		var ent session.Entry
		raw, err := json.Marshal(m["entry"])
		if err != nil || json.Unmarshal(raw, &ent) != nil {
			return nil
		}
		idx := intOf(m["idx"])
		we := WireEntry(idx, &ent, c.a.spill(e.Session))
		out := []wire.Envelope{c.envelope(e.Session, wire.EvEntryAppended, e.Seq, ent.Turn, wire.EntryAppended{Entry: we})}
		if ent.Item.Kind == session.KindCompaction && ent.Item.Compaction != nil { // 压缩点前移：与条目同一个游标
			out = append(out, c.envelope(e.Session, wire.EvLogCompacted, e.Seq, ent.Turn,
				wire.LogCompacted{Upto: ent.Item.Compaction.Upto, EntryIdx: idx}))
		}
		return out
	case store.EventRewind:
		return []wire.Envelope{c.envelope(e.Session, wire.EvLogRewound, e.Seq, 0,
			wire.LogRewound{To: intOf(m["to"]), VoidedInteractions: []string{}})}
	case store.EventRunState:
		s, _ := m["run_state"].(string)
		return []wire.Envelope{c.envelope(e.Session, wire.EvStateChanged, e.Seq, 0, wire.StateChanged{RunState: wireRun(s)})}
	case store.EventSettings:
		model, _ := m["model"].(string)
		reasoning, _ := m["reasoning"].(string)
		return []wire.Envelope{c.envelope(e.Session, wire.EvSettingsChanged, e.Seq, 0,
			wire.SettingsChanged{Settings: wire.Settings{MaxSteps: uint32(intOf(m["max_steps"])), Model: model, Reasoning: reasoning}})}
	case store.EventInteractionOpened: // 载荷是宿主登记时生成的线上 Interaction；子会话的也写在根会话上
		var it wire.Interaction
		if raw, err := json.Marshal(m); err != nil || json.Unmarshal(raw, &it) != nil {
			return nil
		}
		if it.Responders == nil {
			it.Responders = []string{}
		}
		if ap := it.Approval; ap != nil && c.a.o.Modes != nil {
			ap.SessionRules, ap.Blocked = c.a.o.Modes.SessionAllow(ap.Tool, ap.Args)
		}
		return []wire.Envelope{c.envelope(e.Session, wire.EvInteractionOpen, e.Seq, it.Turn, it)}
	case store.EventInteractionResolved:
		id, _ := m["id"].(string)
		sid, _ := m["session"].(string)
		outcome, _ := m["outcome"].(string)
		by, _ := m["by"].(string)
		return []wire.Envelope{c.envelope(e.Session, wire.EvInteractionDone, e.Seq, 0,
			wire.InteractionResolved{ID: id, Session: sid, Outcome: outcome, By: by})}
	case store.EventForked:
		from, _ := m["from"].(string)
		return []wire.Envelope{c.envelope(e.Session, wire.EvSessionForked, e.Seq, 0,
			wire.SessionForked{From: from, Upto: intOf(m["upto"])})}
	case store.EventClosed:
		reason, _ := m["reason"].(string)
		return []wire.Envelope{c.envelope(e.Session, wire.EvSessionClosed, e.Seq, 0, wire.SessionClosed{Reason: reason})}
	case store.EventMeta:
		title, _ := m["title"].(string)
		meta, _ := json.Marshal(m["meta"])
		return []wire.Envelope{c.envelope(e.Session, wire.EvSessionUpdated, e.Seq, 0, wire.SessionUpdated{Title: title, Pinned: pinnedOf(meta)})}
	case store.EventExt:
		if md := c.modeOf(e.Session, m); md != nil {
			return []wire.Envelope{c.envelope(e.Session, wire.EvModeChanged, e.Seq, 0, *md)}
		}
		return nil // 其余 Ext 键是内部状态，不上线
	case EventTurnEnd:
		end := session.EndReason{Kind: session.EndKind(intOf(m["end_kind"]))}
		end.Code, _ = m["code"].(string)
		end.Message, _ = m["message"].(string)
		turn := uint32(intOf(m["turn"]))
		return []wire.Envelope{c.envelope(e.Session, wire.EvTurnEnded, 0, turn, WireTurnEnded(turn, end))}
	case EventTurnDenied:
		reason, _ := m["reason"].(string)
		return []wire.Envelope{c.envelope(e.Session, wire.EvTurnRejected, 0, 0,
			wire.TurnRejected{Inputs: stringList(m["inputs"]), Reason: reason})}
	case EventNotice:
		text, _ := m["text"].(string)
		return []wire.Envelope{c.envelope(e.Session, wire.EvNotice, 0, 0, wire.Notice{Level: "info", Text: text})}
	}
	return nil
}

// modeOf Ext 事件是档位键时给出档位（标签从档位表取；沙箱级别只有当前档位才知道）。
func (c *Conn) modeOf(id string, m map[string]any) *wire.Mode {
	md := c.a.o.Modes
	if md == nil {
		return nil
	}
	key, _ := md.ExtKeys()
	if k, _ := m["key"].(string); k != key {
		return nil
	}
	v, _ := m["value"].(string)
	out := &wire.Mode{Mode: v, Label: v}
	for _, d := range md.List() {
		if d.ID == v {
			out.Label = d.Label
		}
	}
	if st := md.Get(id); st.Mode == v {
		out.Sandbox = st.Sandbox
	}
	return out
}

// watchSessions 连接级 sessions.changed：本进程任一会话的运行状态、标题与置顶变化或删除时刷新列表里的一行
// （易失，重连后重新查列表）。
func (c *Conn) watchSessions(sub *bus.Sub) {
	for e := range sub.C() {
		var row wire.SessionsChanged
		switch {
		case e.Kind == EventDeleted:
			row = wire.SessionsChanged{ID: e.Session, RunState: wire.RunIdle, UpdatedAt: e.At, Deleted: true}
		case (e.Kind == store.EventRunState || e.Kind == store.EventMeta) && e.Seq > 0:
			l, err := host.LoadHead(c.ctx, c.a.o.DB, e.Session)
			if err != nil {
				continue
			}
			row = wire.SessionsChanged{ID: e.Session, RunState: wireRun(l.RunState), Title: l.Meta.Title,
				Pinned: pinnedOf(l.Meta.Meta), UpdatedAt: e.At}
			if e.Kind == store.EventRunState {
				s, _ := dataMap(e.Data)["run_state"].(string)
				row.RunState = wireRun(s)
			} else {
				row.UpdatedAt = 0 // 改标题、置顶不改更新时间：客户端保留原值
			}
		default:
			continue
		}
		c.push(c.ctx, []wire.Envelope{c.envelope("", wire.EvSessionsChanged, 0, 0, row)})
	}
}

func (c *Conn) envelope(id, kind string, cursor int64, turn uint32, data any) wire.Envelope {
	return wire.Envelope{V: wire.Version, Session: id, Kind: kind, At: time.Now().UnixMilli(), Cursor: cursor, Turn: turn, Data: data}
}

// push 进出站队列，必要时按背压规则降级。实时事件不等：慢就降级、再慢就断开订阅。
// ctx 是事件所属订阅的作用域：取消（退订、被新订阅替换、lagged）在 c.mu 下进行，这里持锁检查，已取消的订阅不会再漏进事件。
func (c *Conn) push(ctx context.Context, evs []wire.Envelope) {
	if len(evs) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || ctx.Err() != nil {
		return
	}
	c.queue = append(c.queue, evs...)
	soft, hard := c.limits()
	if len(c.queue) > soft {
		c.queue = compact(c.queue)
	}
	for len(c.queue) > hard && c.lagLocked(hard) {
	}
	c.signalLocked()
}

// pushWait 回放用：历史事件不降级，队列满时等传输层取走（消费者在追历史，不算过慢）。
func (c *Conn) pushWait(ctx context.Context, evs []wire.Envelope) bool {
	for {
		c.mu.Lock()
		if c.closed || ctx.Err() != nil {
			c.mu.Unlock()
			return false
		}
		soft, _ := c.limits()
		if len(c.queue) < soft {
			c.queue = append(c.queue, evs...)
			c.signalLocked()
			c.mu.Unlock()
			return true
		}
		space := c.space
		c.mu.Unlock()
		select {
		case <-space:
		case <-ctx.Done():
			return false
		}
	}
}

func (c *Conn) signalLocked() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// lag 断开会话 id 的订阅并发 session.lagged。
func (c *Conn) lag(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lagSessionLocked(id)
	c.signalLocked()
}

// lagLocked 断开积压最多的订阅；没有可断开的（只剩连接级事件）返回 false。
func (c *Conn) lagLocked(hard int) bool {
	count := map[string]int{}
	for _, e := range c.queue {
		if _, ok := c.subs[e.Session]; ok {
			count[e.Session]++
		}
	}
	top, n := "", 0
	for id, k := range count {
		if k > n {
			top, n = id, k
		}
	}
	if top == "" {
		c.queue = c.queue[len(c.queue)-hard:] // 只剩连接级易失事件：丢最旧的
		return false
	}
	c.lagSessionLocked(top)
	return true
}

func (c *Conn) lagSessionLocked(id string) {
	if s := c.subs[id]; s != nil {
		s.cancel()
		delete(c.subs, id)
	}
	c.dropLocked(id)
	c.queue = append(c.queue, c.envelope(id, wire.EvLagged, 0, 0, wire.CursorRef{Cursor: c.delivered[id]}))
}

// dropLocked 丢掉队列里会话 id 的事件。
func (c *Conn) dropLocked(id string) {
	q := c.queue[:0]
	for _, e := range c.queue {
		if e.Session != id {
			q = append(q, e)
		}
	}
	c.queue = q
}

// Next 等到有事件后一次取走全部（按序）；连接关闭或 ctx 结束返回错误。
func (c *Conn) Next(ctx context.Context) ([]wire.Envelope, error) {
	for {
		c.mu.Lock()
		if len(c.queue) > 0 {
			out := c.queue
			c.queue = nil
			for _, e := range out {
				if e.Cursor > 0 && e.Cursor > c.delivered[e.Session] {
					c.delivered[e.Session] = e.Cursor
				}
			}
			close(c.space)
			c.space = make(chan struct{})
			c.mu.Unlock()
			return out, nil
		}
		closed := c.closed
		c.mu.Unlock()
		if closed {
			return nil, fmt.Errorf("%w: 连接已关闭", host.ErrClosed)
		}
		select {
		case <-c.wake:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// compact 背压第 1、2 级：合并同一 stream_id 的 message.delta 与同一 call_id 的 tool.args_delta（文本拼到第一条上），
// 丢弃 tool.progress 与 notice。持久事件不动。
func compact(q []wire.Envelope) []wire.Envelope {
	out := q[:0]
	firstDelta := map[string]int{}
	firstArgs := map[string]int{}
	for _, e := range q {
		switch d := e.Data.(type) {
		case wire.MessageDelta:
			if i, ok := firstDelta[e.Session+"\x00"+d.StreamID]; ok {
				p := out[i].Data.(wire.MessageDelta)
				p.Text += d.Text
				out[i].Data = p
				continue
			}
			firstDelta[e.Session+"\x00"+d.StreamID] = len(out)
		case wire.ToolArgsDelta:
			if i, ok := firstArgs[e.Session+"\x00"+d.CallID]; ok {
				p := out[i].Data.(wire.ToolArgsDelta)
				p.Text += d.Text
				out[i].Data = p
				continue
			}
			firstArgs[e.Session+"\x00"+d.CallID] = len(out)
		case wire.ToolProgress, wire.Notice:
			continue
		}
		out = append(out, e)
	}
	return out
}

// dataMap 总线事件的 data 统一成 map（提交通知是 map，回放的其余种类是 json.RawMessage）。
func dataMap(v any) map[string]any {
	switch d := v.(type) {
	case map[string]any:
		return d
	case json.RawMessage:
		var m map[string]any
		_ = json.Unmarshal(d, &m)
		return m
	case nil:
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

func intOf(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case uint32:
		return int(n)
	case float64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	}
	return 0
}

// ConnCommands 连接上可用的全部命令（Call 的命令加上连接级的 session.subscribe / unsubscribe），排序后。
// 前端命令表与它一致（apps/desktop 的测试校验）。
func ConnCommands() []string {
	out := append(Commands(), "session.subscribe", "session.unsubscribe")
	sort.Strings(out)
	return out
}
