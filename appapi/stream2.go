package appapi

// 分段流式事件（protocol 2，docs/06 - 应用协议契约.md 10.5 表二）：agent.Hooks.OnStream / OnToolCall / OnToolResult
// → 总线上的 message.* / tool.* 事件（data 是 v2Event，Conn.translate 原样上线），同时维护快照的 live（10.2.2 规则 7）。
//
// 关联：message.started / tool.started 带本步 assistant 条目的 entry_id；每段（text / reasoning）每次尝试一个 stream_id；
// 模型重试时旧段发 message.discarded，新段 attempt 加一。正文与参数增量按 BatchInterval 合批，工具事件之前先发完积压的增量，
// 保证顺序。live 只读内存、不承诺持久：新的一步或回合结束时清空。

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/google/uuid"

	"tenon/appapi/wire"
	"tenon/runtime/agent"
	"tenon/runtime/bus"
	"tenon/runtime/session"
)

// v2Event 总线上 protocol 2 易失事件的 data：回合号 + 线上载荷。
type v2Event struct {
	Turn    uint32
	Payload any
}

// liveStream 一段进行中的输出。
type liveStream struct {
	id, part string
	text     []byte
}

type liveTool struct {
	id, name string
	args     []byte
}

// pending 一条待合批发出的增量（message.delta 或 tool.args_delta）。
type pending struct {
	stream bool
	id     string
	text   []byte
}

// liveState 一个会话的流式状态。
type liveState struct {
	b  *bus.Bus
	id string

	mu      sync.Mutex
	turn    uint32
	entry   string
	attempt int
	streams map[string]*liveStream // part → 当前段
	order   []*liveStream
	tools   map[string]*liveTool // call_id → 调用
	torder  []*liveTool
	queue   []*pending
	timer   *time.Timer
}

func newLiveState(b *bus.Bus, id string) *liveState {
	return &liveState{b: b, id: id, streams: map[string]*liveStream{}, tools: map[string]*liveTool{}}
}

// publishLocked 持锁发布：并发的合批计时器不会把事件发乱序。
func (l *liveState) publishLocked(kind string, payload any) {
	l.b.Publish(bus.Envelope{Session: l.id, Kind: kind, Data: v2Event{Turn: l.turn, Payload: payload}})
}

func (l *liveState) flushLocked() {
	if l.timer != nil {
		l.timer.Stop()
		l.timer = nil
	}
	for _, p := range l.queue {
		if p.stream {
			l.publishLocked(wire.EvMessageDelta, wire.MessageDelta{StreamID: p.id, Text: string(p.text)})
		} else {
			l.publishLocked(wire.EvToolArgsDelta, wire.ToolArgsDelta{CallID: p.id, Text: string(p.text)})
		}
	}
	l.queue = nil
}

func (l *liveState) flush() {
	l.mu.Lock()
	l.flushLocked()
	l.mu.Unlock()
}

// enqueueLocked 追加增量：与队尾同一目标时拼接，否则新起一条；首条启动合批计时器。
func (l *liveState) enqueueLocked(stream bool, id, text string) {
	if n := len(l.queue); n > 0 && l.queue[n-1].stream == stream && l.queue[n-1].id == id {
		l.queue[n-1].text = append(l.queue[n-1].text, text...)
	} else {
		l.queue = append(l.queue, &pending{stream: stream, id: id, text: []byte(text)})
	}
	if l.timer == nil {
		l.timer = time.AfterFunc(BatchInterval, l.flush)
	}
}

// clearLocked 丢掉本步的流式状态（新的一步、回合结束）。
func (l *liveState) clearLocked() {
	l.streams, l.order = map[string]*liveStream{}, nil
	l.tools, l.torder = map[string]*liveTool{}, nil
}

func (l *liveState) onStream(ev agent.StreamEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch ev.Kind {
	case agent.StreamStep:
		l.flushLocked()
		l.clearLocked()
		l.turn, l.entry, l.attempt = ev.Turn, ev.EntryID, ev.Attempt
	case agent.StreamText, agent.StreamReasoning:
		part := "text"
		if ev.Kind == agent.StreamReasoning {
			part = "reasoning"
		}
		s := l.streams[part]
		if s == nil {
			l.flushLocked()
			u, _ := uuid.NewV7()
			s = &liveStream{id: u.String(), part: part}
			l.streams[part] = s
			l.order = append(l.order, s)
			l.publishLocked(wire.EvMessageStarted, wire.MessageStarted{StreamID: s.id, EntryID: l.entry, Part: part, Attempt: l.attempt})
		}
		s.text = append(s.text, ev.Text...)
		l.enqueueLocked(true, s.id, ev.Text)
	case agent.StreamTool:
		t := l.startToolLocked(ev.CallID, ev.Name)
		if ev.Args != "" {
			t.args = append(t.args, ev.Args...)
			l.enqueueLocked(false, t.id, ev.Args)
		}
	case agent.StreamReset:
		l.flushLocked()
		for _, s := range l.order {
			l.publishLocked(wire.EvMessageDiscarded, wire.StreamRef{StreamID: s.id})
		}
		l.clearLocked()
		l.attempt = ev.Attempt
	case agent.StreamProgress:
		if l.tools[ev.CallID] == nil { // 工具已结束（结果先到）或不认识：丢掉
			return
		}
		l.flushLocked()
		l.publishLocked(wire.EvToolProgress, wire.ToolProgress{CallID: ev.CallID, Text: ev.Text})
	case agent.StreamEnd:
		l.flushLocked()
		for _, s := range l.order {
			l.publishLocked(wire.EvMessageEnded, wire.StreamRef{StreamID: s.id})
		}
	}
}

// startToolLocked 第一次见到调用时发 tool.started（之前积压的增量先发完）。
func (l *liveState) startToolLocked(id, name string) *liveTool {
	if t := l.tools[id]; t != nil {
		if t.name == "" {
			t.name = name
		}
		return t
	}
	l.flushLocked()
	t := &liveTool{id: id, name: name}
	l.tools[id] = t
	l.torder = append(l.torder, t)
	l.publishLocked(wire.EvToolStarted, wire.ToolStarted{CallID: id, Name: name, EntryID: l.entry})
	return t
}

// onToolCall 工具开始执行：参数已完整。没有流式片段的模型在这里补发 tool.started。
func (l *liveState) onToolCall(c session.ToolCall) {
	l.mu.Lock()
	defer l.mu.Unlock()
	t := l.startToolLocked(c.ID, c.Name)
	l.flushLocked()
	t.args = append(t.args[:0], c.Args...)
	l.publishLocked(wire.EvToolRunning, wire.ToolRunning{CallID: c.ID, Args: jsonValue(c.Args)})
}

func (l *liveState) onToolResult(c session.ToolCall, out string, isErr bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.flushLocked()
	st := "ok"
	if isErr {
		st = "error"
	}
	l.publishLocked(wire.EvToolEnded, wire.ToolEnded{CallID: c.ID, Status: st, Preview: Preview(out, PreviewLen)})
	if t := l.tools[c.ID]; t != nil {
		delete(l.tools, c.ID)
		for i, x := range l.torder {
			if x == t {
				l.torder = append(l.torder[:i], l.torder[i+1:]...)
				break
			}
		}
	}
}

// endTurn 回合结束：发完积压的增量、清空 live（在 turn.ended 之前调用）。
func (l *liveState) endTurn() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.flushLocked()
	l.clearLocked()
	l.turn, l.entry = 0, ""
}

// snapshot 当前步已累积的流式状态；没有进行中的步返回 nil。
func (l *liveState) snapshot() *wire.Live {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.entry == "" {
		return nil
	}
	out := &wire.Live{Turn: l.turn, Streams: []wire.LiveStream{}, Tools: []wire.LiveTool{}}
	for _, s := range l.order {
		out.Streams = append(out.Streams, wire.LiveStream{StreamID: s.id, EntryID: l.entry, Part: s.part, Text: string(s.text)})
	}
	for _, t := range l.torder {
		lt := wire.LiveTool{CallID: t.id, Name: t.name}
		if len(t.args) > 0 && json.Valid(t.args) {
			lt.Args = append(json.RawMessage(nil), t.args...)
		}
		out.Tools = append(out.Tools, lt)
	}
	return out
}

// liveOf 会话的流式状态（StreamHooks 建的）；没有返回 nil。
func (a *App) liveOf(id string) *liveState {
	if v, ok := a.lives.Load(id); ok {
		return v.(*liveState)
	}
	return nil
}
