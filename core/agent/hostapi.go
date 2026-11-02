package agent

// 会话宿主（host 包）用的入口：带 meta 的输入、续跑、Idle 下的提交、span。

import (
	"context"

	"ybase-kernel/core/session"
)

// SendContent 用给定内容与 meta 开回合；consume 为本回合消费的 inbox 项，随回合开始的 checkpoint 同事务删除（H7）。
func (a *Agent) SendContent(ctx context.Context, c session.Content, meta map[string]any, consume []string) (session.EndReason, error) {
	_, need, err := a.Core.Input(c, meta)
	if err != nil {
		return session.EndReason{}, err
	}
	a.consume = append(a.consume, consume...)
	return a.drive(ctx, need)
}

// Drive 驱动一个已有需求（Resume 之后）。
func (a *Agent) Drive(ctx context.Context, need *session.Need) (session.EndReason, error) {
	return a.drive(ctx, need)
}

// Consume 把已处理的 inbox 项记入下一次 checkpoint（回合中插入用户消息时用，H7）。
func (a *Agent) Consume(ids ...string) { a.consume = append(a.consume, ids...) }

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
	if err != nil || need == nil {
		return session.DoneReason(), nil // 没有可续跑的回合：视为没有重跑内容
	}
	return a.drive(ctx, need)
}

// CommitRun 提交 Idle 下的修改并显式指定 run_state（suspended 用于预算挂起，02 2.2）。
func (a *Agent) CommitRun(ctx context.Context, run string, consume ...string) error {
	a.consume = append(a.consume, consume...)
	return a.checkpoint(ctx, run, nil)
}

// Commit Idle 下的修改（record / set_ext / compact / rewind）与头部同事务提交（03 §8.2 第 4 条）。
func (a *Agent) Commit(ctx context.Context, consume ...string) error {
	a.consume = append(a.consume, consume...)
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
