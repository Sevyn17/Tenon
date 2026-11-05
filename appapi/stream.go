package appapi

// 流式事件（§四 易失事件）：agent.Hooks → 总线事件 text_delta / text_reset / tool_call / tool_result。
// 正文增量按会话合批（BatchInterval）后再发，避免每个 token 一个事件；工具事件与回合结束前先把积压的正文发出去，保证顺序。

import (
	"strings"
	"sync"
	"time"

	"ybase/runtime/agent"
	"ybase/runtime/bus"
	"ybase/runtime/session"
)

// 易失事件类型（宿主发的 turn_start / turn_end / turn_denied / human_requested 之外由本层发的）。
const (
	EventTextDelta  = "text_delta"  // {text}
	EventTextReset  = "text_reset"  // 模型重试，丢弃本步已收到的正文
	EventToolCall   = "tool_call"   // {call_id, name, args}
	EventToolResult = "tool_result" // {call_id, preview, error}
	EventAsk        = "ask"         // {ask_id, question, options}
	EventNotice     = "notice"      // {text}
)

// 宿主发的易失事件类型（runtime/host）。
const (
	EventTurnStart      = "turn_start"
	EventTurnEnd        = "turn_end"
	EventTurnDenied     = "turn_denied"
	EventHumanRequested = "human_requested"
)

// BatchInterval 正文增量合批间隔（协议约定 16–50 ms）。
const BatchInterval = 30 * time.Millisecond

// PreviewLen tool_result.preview 的最大字符数。
const PreviewLen = 300

// streamer 一个会话的正文合批器。
type streamer struct {
	b     *bus.Bus
	id    string
	mu    sync.Mutex
	buf   strings.Builder
	timer *time.Timer
}

func (s *streamer) text(d string) {
	if d == "" {
		return
	}
	s.mu.Lock()
	s.buf.WriteString(d)
	if s.timer == nil {
		s.timer = time.AfterFunc(BatchInterval, s.flush)
	}
	s.mu.Unlock()
}

// flush 立即发出积压的正文。
func (s *streamer) flush() {
	s.mu.Lock()
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	t := s.buf.String()
	s.buf.Reset()
	if t != "" { // 持锁发布：并发的 flush 不会把两段正文发乱序
		s.b.Publish(bus.Envelope{Session: s.id, Kind: EventTextDelta, Data: map[string]any{"text": t}})
	}
	s.mu.Unlock()
}

func (s *streamer) publish(kind string, data any) {
	s.flush()
	s.b.Publish(bus.Envelope{Session: s.id, Kind: kind, Data: data})
}

func (s *streamer) reset() {
	s.mu.Lock()
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	s.buf.Reset()
	s.mu.Unlock()
	s.b.Publish(bus.Envelope{Session: s.id, Kind: EventTextReset})
}

// StreamHooks 会话 id 的流式钩子：把正文 / 工具调用 / 工具结果发成总线事件。
// New 在给了 Builder 时会自动并入；自己构造 agent 的应用可以手动用 MergeHooks 并入。
func (a *App) StreamHooks(id string) agent.Hooks {
	s := &streamer{b: a.o.Bus, id: id}
	a.streams.Store(id, s)
	l := newLiveState(a.o.Bus, id) // protocol 2：message.* / tool.* 与快照 live（stream2.go）
	a.lives.Store(id, l)
	return agent.Hooks{
		OnReset:  s.reset,
		OnText:   s.text,
		OnStream: l.onStream,
		OnToolCall: func(c session.ToolCall) {
			s.publish(EventToolCall, map[string]any{"call_id": c.ID, "name": c.Name, "args": string(c.Args)})
			l.onToolCall(c)
		},
		OnToolResult: func(c session.ToolCall, out string, isErr bool) {
			s.publish(EventToolResult, map[string]any{"call_id": c.ID, "preview": Preview(out, PreviewLen), "error": isErr})
			l.onToolResult(c, out, isErr)
		},
	}
}

// flush 发出会话 id 积压的正文（回合结束前调用，保证 text_delta 在 turn_end 之前）。
func (a *App) flush(id string) {
	if v, ok := a.streams.Load(id); ok {
		v.(*streamer).flush()
	}
}

// MergeHooks 依次调用两组钩子（任一组的空钩子跳过）。
func MergeHooks(x, y agent.Hooks) agent.Hooks {
	return agent.Hooks{
		OnReset: func() {
			if x.OnReset != nil {
				x.OnReset()
			}
			if y.OnReset != nil {
				y.OnReset()
			}
		},
		OnText: func(d string) {
			if x.OnText != nil {
				x.OnText(d)
			}
			if y.OnText != nil {
				y.OnText(d)
			}
		},
		OnToolCall: func(c session.ToolCall) {
			if x.OnToolCall != nil {
				x.OnToolCall(c)
			}
			if y.OnToolCall != nil {
				y.OnToolCall(c)
			}
		},
		OnToolResult: func(c session.ToolCall, out string, isErr bool) {
			if x.OnToolResult != nil {
				x.OnToolResult(c, out, isErr)
			}
			if y.OnToolResult != nil {
				y.OnToolResult(c, out, isErr)
			}
		},
		OnConfirm: func(callID, tool, reason string) {
			if x.OnConfirm != nil {
				x.OnConfirm(callID, tool, reason)
			}
			if y.OnConfirm != nil {
				y.OnConfirm(callID, tool, reason)
			}
		},
	}
}

// Preview 去掉首尾空白，超过 n 个字符截断加“…”。
func Preview(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
