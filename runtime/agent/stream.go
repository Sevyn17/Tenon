package agent

// 分段流式（protocol 2，docs/06 - 应用协议契约.md 10.5）：一步模型调用的流式事件经 Hooks.OnStream 交给展示层。
//
// 关联规则：
//   - 一步开始时发 step，EntryID 是这一步 assistant 条目将用的 id（= 请求 id，见 session.Core.PendingEntryID），
//     条目落库后 entry.id 与它相同；
//   - 工具调用的流式片段按流内序号预分配调用 id，模型回复到达后写回调用（内核保留已有 id），
//     所以 tool 事件的 CallID 与条目里的调用 id、之后的工具结果一致；
//   - 模型重试时发 reset（Attempt 加一），之前的片段作废、调用 id 重新分配。

import (
	"context"
	"sort"

	"github.com/google/uuid"

	"tenon/runtime/modelhub"
	"tenon/runtime/session"
)

// 流式事件种类。
const (
	StreamStep      = "step"      // 一步开始：Turn、EntryID、Attempt = 1
	StreamText      = "text"      // 正文增量：Text
	StreamReasoning = "reasoning" // 推理增量：Text
	StreamTool      = "tool"      // 工具调用片段：CallID，Name（调用开始的片段），Args（参数增量）
	StreamReset     = "reset"     // 模型重试：本步已发的片段作废，Attempt 加一
	StreamEnd       = "end"       // 本步模型调用结束（成败都发）
	StreamProgress  = "progress"  // 工具执行中的输出：CallID，Text（最新的一段，替换显示）
)

// StreamEvent 一条流式事件。
type StreamEvent struct {
	Kind    string
	Turn    uint32
	EntryID string
	Attempt int
	Text    string
	CallID  string
	Name    string
	Args    string
}

// SinkModel 可选：带推理增量与工具调用片段的调用（modelhub.Session 实现），优先于 RequestModel。
type SinkModel interface {
	StreamSink(ctx context.Context, requestID string, req session.Request, sk modelhub.Sink) (session.AssistantItem, session.StopReason, error)
}

// stepStream 一步模型调用的流式状态。没有 OnStream 时什么都不做（也不预分配调用 id）。
type stepStream struct {
	emit    func(StreamEvent)
	turn    uint32
	entry   string
	attempt int
	calls   map[int]string // 流内序号 → 预分配的调用 id
}

func (a *Agent) newStepStream(need *session.Need, retried bool) *stepStream {
	s := &stepStream{emit: a.Hooks.OnStream, turn: need.Model.Turn, entry: a.Core.PendingEntryID(), attempt: 1,
		calls: map[int]string{}}
	if s.emit == nil {
		return s
	}
	if s.entry == "" {
		s.entry = need.Model.RequestID
	}
	if retried { // 上下文超限压缩后重发：同一步、同一个条目，按重试处理
		s.reset()
		return s
	}
	s.emit(StreamEvent{Kind: StreamStep, Turn: s.turn, EntryID: s.entry, Attempt: 1})
	return s
}

func (s *stepStream) send(ev StreamEvent) {
	if s.emit == nil {
		return
	}
	ev.Turn, ev.EntryID, ev.Attempt = s.turn, s.entry, s.attempt
	s.emit(ev)
}

func (s *stepStream) text(d string)      { s.send(StreamEvent{Kind: StreamText, Text: d}) }
func (s *stepStream) reasoning(d string) { s.send(StreamEvent{Kind: StreamReasoning, Text: d}) }

func (s *stepStream) tool(td modelhub.ToolDelta) {
	if s.emit == nil {
		return
	}
	id, ok := s.calls[td.Index]
	if !ok {
		u, _ := uuid.NewV7()
		id = u.String()
		s.calls[td.Index] = id
	}
	s.send(StreamEvent{Kind: StreamTool, CallID: id, Name: td.Name, Args: td.Args})
}

func (s *stepStream) reset() {
	if s.emit == nil {
		return
	}
	s.attempt++
	s.calls = map[int]string{}
	s.send(StreamEvent{Kind: StreamReset})
}

func (s *stepStream) end() { s.send(StreamEvent{Kind: StreamEnd}) }

// assign 把预分配的调用 id 写回回复：流内序号升序即回复里调用的顺序（三个适配器都按块 / 项序号组装调用）。
// 数量对不上（适配器没发片段或发得不全）时不写，调用 id 照常由内核分配。
func (s *stepStream) assign(reply *session.AssistantItem) {
	if len(s.calls) == 0 || len(s.calls) != len(reply.ToolCalls) {
		return
	}
	idx := make([]int, 0, len(s.calls))
	for i := range s.calls {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	for k, i := range idx {
		if reply.ToolCalls[k].ID == "" {
			reply.ToolCalls[k].ID = s.calls[i]
		}
	}
}
