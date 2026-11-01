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
