package agent

// 会话宿主（host 包）用的入口：带 meta 的输入、续跑、Idle 下的提交、span。

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"ybase/runtime/mail"
	"ybase/runtime/session"
	"ybase/runtime/store"
)

// SendContent 用给定内容与 meta 开回合；consume 为本回合消费的 inbox 项，随回合开始的 checkpoint 同事务删除（H7）。
func (a *Agent) SendContent(ctx context.Context, c session.Content, meta map[string]any, consume []string) (session.EndReason, error) {
	_, need, err := a.Core.Input(c, meta)
	if err != nil {
		return session.EndReason{}, err
	}
	a.buf.consume = append(a.buf.consume, consume...)
	return a.drive(ctx, need)
}

// Drive 驱动一个已有需求（Resume 之后）。
func (a *Agent) Drive(ctx context.Context, need *session.Need) (session.EndReason, error) {
	return a.drive(ctx, need)
}

// Consume 把已处理的 inbox 项记入下一次 checkpoint（回合中插入用户消息时用，H7）。
func (a *Agent) Consume(ids ...string) { a.buf.consume = append(a.buf.consume, ids...) }

// Retry 从当前检查点重跑未完成的一轮（所属树策略 restart，02 2.2）。
// 回合有未满足的需求（WaitingModel / WaitingTools）时重发/续答；失败的一轮已收尾、最后一条是可续位置时按 Resume 重开。
// 两者都不成立返回 Done（没有可重跑的内容）。
func (a *Agent) Retry(ctx context.Context) (session.EndReason, error) {
	if need := a.Core.Resumed(); need != nil {
		return a.drive(ctx, need)
	}
	if a.Core.Status() != session.StatusIdle {
		return session.DoneReason(), nil
	}
	_, need, err := a.Core.Resume()
	if errors.Is(err, session.ErrNoTurn) || errors.Is(err, session.ErrBadResume) || (err == nil && need == nil) {
		return session.DoneReason(), nil // 没有可续跑的回合：视为没有重跑内容
	}
	if err != nil {
		// 只读、损坏等其他错误向上报：所属树的重启策略要分清"无可重跑"和"会话坏了"（审查 P1-22）。
		return session.EndReason{}, err
	}
	return a.drive(ctx, need)
}

// CommitRun 提交 Idle 下的修改并显式指定 run_state（suspended 用于预算挂起，02 2.2）。
func (a *Agent) CommitRun(ctx context.Context, run string, consume ...string) error {
	a.buf.consume = append(a.buf.consume, consume...)
	return a.checkpoint(ctx, run, nil)
}

// Commit Idle 下的修改（record / set_ext / compact / rewind）与头部同事务提交（03 §8.2 第 4 条）。
func (a *Agent) Commit(ctx context.Context, consume ...string) error {
	a.buf.consume = append(a.buf.consume, consume...)
	run := "idle"
	if a.Core.Status() != session.StatusIdle {
		run = "running"
	}
	return a.checkpoint(ctx, run, nil)
}

// StartSpan 开一个 span（宿主记 turn span）。
func (a *Agent) StartSpan(kind, name, parent string) *Span {
	return (*Span)(a.startSpan(kind, name, parent))
}

// Span 导出的 span 句柄。
type Span span

// Set 记属性。
func (s *Span) Set(k string, v any) *Span { (*span)(s).Set(k, v); return s }

// End 结束。
func (s *Span) End(status string) { (*span)(s).End(status) }

// ID span id（子 span 的 parent）。
func (s *Span) ID() string { return s.s.ID }

// Outbound 以本会话身份发一封信（会话之间投递的唯一入口）：内核字段由这里写，调用方给的忽略
// （08 8.3 / 8.7，09 9.6 D2）——
//   - from = session:<本会话>；
//   - hop = 触发本回合的信封里最大的跳数（派发时再加一）；reply / control 同样沿用，派发不另减；
//   - trace = 本回合的 trace；
//   - caps = 本会话这一轮的有效权限：可用工具集 ∩ 本轮 caps（被调用方拿不到发送方没有的工具）；
//   - origin：本回合由不可信来源触发时记 external，否则 agent（只降不升）。
//
// 信封随本轮检查点落库，提交后由宿主派发（08 9.6）。
func (a *Agent) Outbound(env mail.Envelope) error {
	o := a.CurrentTurnOrigin()
	env.From = mail.Address{Kind: mail.AddrSession, ID: a.SessionID}
	env.Hop = o.Hop
	env.Trace = a.TraceID()
	env.Caps = a.effectiveCaps()
	env.Origin = mail.OriginAgent
	if o.Untrusted {
		env.Origin = mail.OriginExternal
	}
	env.Via = ""
	if err := env.Normalize(); err != nil {
		return err
	}
	b, err := json.Marshal(env)
	if err != nil {
		return err
	}
	a.EnqueueOutbox(env.ID, b)
	return nil
}

// effectiveCaps 本会话这一轮能用的工具：可用工具集 ∩ 本轮 caps。总是返回非 nil
// （会话之间的信封必须带上发送方的权限，「不限」不能沿链传下去）。
func (a *Agent) effectiveCaps() []string {
	var avail []string
	if a.Toolset != nil {
		avail = a.Toolset.Available()
	}
	caps := a.Caps()
	out := []string{}
	for _, n := range avail {
		if caps != nil && !contains(caps, n) {
			continue
		}
		out = append(out, n)
	}
	return out
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// EnqueueOutbox 把一封待外发的信封记入本轮检查点：它与历史、工具结果在同一次提交里
// 落库（08 9.6），提交成功后才由 Host 派发，崩溃时不会出现「信发了但回合没落库」。
// 信封由调用方序列化成 JSON（通常来自 mail.Envelope）。
func (a *Agent) EnqueueOutbox(id string, envelope []byte) {
	a.buf.outbox = append(a.buf.outbox, store.OutboxItem{
		ID:        id,
		Envelope:  append([]byte(nil), envelope...),
		CreatedAt: time.Now().UnixMilli(),
	})
}

// TurnOrigin 本回合的触发链信息（发出信封时由内核盖到信封上，见 host.StampOutbound）：
// 触发本回合的信封里最大的跳数，以及触发来源是否不可信。host 在开回合时设置。
type TurnOrigin struct {
	Hop       int
	Untrusted bool
}

// SetTurnOrigin 设置本回合的触发链信息。
func (a *Agent) SetTurnOrigin(o TurnOrigin) {
	a.trace.setOrigin(o)
}

// CurrentTurnOrigin 本回合的触发链信息。
func (a *Agent) CurrentTurnOrigin() TurnOrigin {
	return a.trace.getOrigin()
}

// SetTrace 设置当前回合的 trace id（host 在取信时从信封上拿到；为空表示不记录）。
func (a *Agent) SetTrace(trace string) {
	a.trace.setID(trace)
}

// TraceID 返回当前回合的 trace id。
func (a *Agent) TraceID() string {
	return a.trace.getID()
}

// AddTraceEvent 记一条 trace 事件，等本回合结束时由 host 落库（避免和 checkpoint 抢写）。
func (a *Agent) AddTraceEvent(kind, detail string) {
	a.trace.add(a.SessionID, kind, detail)
}

// TakeTraceEvents 取走缓存的 trace 事件。
func (a *Agent) TakeTraceEvents() []store.TraceEvent {
	return a.trace.take()
}

// TakeUnconsumed 取走上一回合结束时没进模型的插话（取消、中断、失败时内核原样交还，T17 / 审查 P1-19）。
func (a *Agent) TakeUnconsumed() []session.PendingInput {
	u := a.unconsumed
	a.unconsumed = nil
	return u
}
