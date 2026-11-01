// Package agent：驱动 session 状态机的最小主循环。
//
// 循环只做三件事：按 Need 调模型、执行工具、把事实交回内核；每次停顿一个 checkpoint 事务（persist.go）。
// 完整的会话宿主（actor、命令队列、钩子）在 M4 取代本包。
package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"ybase-kernel/core/modelhub"
	"ybase-kernel/core/reqlog"
	"ybase-kernel/core/session"
	"ybase-kernel/core/store"
	"ybase-kernel/core/toolhub"
)

// Model 模型调用接口（modelhub.Session 实现；测试用假实现）。
type Model interface {
	Stream(ctx context.Context, req session.Request, onText func(string)) (session.AssistantItem, session.StopReason, error)
}

// RequestModel 可选：带 need id 的调用（modelhub.Session 实现），优先于 Stream。
type RequestModel interface {
	StreamRequest(ctx context.Context, requestID string, req session.Request,
		onText func(string), onReset func()) (session.AssistantItem, session.StopReason, error)
}

// Compactor 压缩决策（compact.Compactor 实现）：需要压缩时返回视图与条目 meta，否则 nil。
type Compactor interface {
	Plan(ctx context.Context, sessionID string, core *session.Core, force bool) (*reqlog.CompactionView, map[string]any, error)
}

// Hooks 展示与审批回调（均可为 nil）。
type Hooks struct {
	OnReset      func() // 重试 / 降级前：已显示的增量作废
	OnText       func(delta string)
	OnToolCall   func(call session.ToolCall)
	OnToolResult func(call session.ToolCall, output string, isErr bool)
	OnConfirm    func(callID, tool, reason string) // 挂起式确认开始（HumanRequested）
}

// Agent 一个会话的运行体（非并发安全，单一调用方）。
type Agent struct {
	Core      *session.Core
	Model     Model
	Hooks     Hooks
	Store     store.Store // nil = 不持久化
	SessionID string

	committed int              // 存储里已提交的 log_len（条件写期望值）
	resume    *session.Need    // Open 恢复出的待满足需求
	recovered *session.Request // 恢复 WaitingModel 时按请求记录重组出的请求（原样重发）
	// recoveredView 恢复出的请求所用压缩视图（重发时记录不变）。
	recoveredView *reqlog.CompactionView

	// Compactor 可空：每次模型调用前决定是否压缩（compact-basic 插件，最外层）。
	Compactor Compactor

	// Now 时钟（测试注入）；nil = time.Now。
	Now func() time.Time

	pendingSpans []store.Span // 待随下一次 checkpoint 提交的 span（G2）

	// Hub 工具管理器（必需：调用栈、并行、幂等、确认、存工件）；Toolset 为会话工具视图（可空）。
	Hub     *toolhub.Hub
	Toolset *toolhub.Toolset

	spanMu   sync.Mutex
	hubSpans []store.Span

	// TurnSpanID 宿主设置的当前 turn span（step span 的父）；stepID 当前 step span（attempt / tool 的父）。
	TurnSpanID string
	step       *span
	stepID     string

	// Between 可空：每个需求执行前调用（会话宿主在这里应用回合中途的命令），返回替换后的需求。
	Between func(need *session.Need) (*session.Need, error)

	consume []string // 随下一次 checkpoint 删除的 inbox 项（session-host H7：core.Input 成功后同事务删除）

	// crashAt 故障注入（测试）：在指定 checkpoint 点提交后返回 errCrash，模拟进程被杀。
	crashAt string
}

var errCrash = errors.New("agent: 注入的崩溃")

// Send 开启一个回合并跑到结束。
func (a *Agent) Send(ctx context.Context, text string) (session.EndReason, error) {
	_, need, err := a.Core.Input(session.Content{session.NewTextPart(text)}, map[string]any{session.MetaSource: "user"})
	if err != nil {
		return session.EndReason{}, err
	}
	return a.drive(ctx, need)
}

func (a *Agent) drive(ctx context.Context, need *session.Need) (session.EndReason, error) {
	for {
		var err error
		// 需求边界：应用回合进行中投递的命令（插话 / 改向 / 配置 / 压缩），可能替换当前需求。
		if a.Between != nil && need.Kind != session.NeedDone {
			if need, err = a.Between(need); err != nil {
				return session.EndReason{}, err
			}
		}
		switch need.Kind {
		case session.NeedDone:
			a.endStep("ok")
			if err := a.point(ctx, "turn_end", "idle", nil); err != nil {
				return session.EndReason{}, err
			}
			return need.Done.Reason, nil

		case session.NeedModel:
			a.endStep("ok")
			a.beginStep(need.Model.Step)
			need, err = a.callModel(ctx, need)

		case session.NeedTools:
			if err := a.point(ctx, "before_tool", "running", nil); err != nil { // S5
				return session.EndReason{}, err
			}
			if ctx.Err() != nil {
				_, need, err = a.Core.Cancel()
				break
			}
			if a.Hub == nil {
				return session.EndReason{}, errors.New("agent: 未配置工具管理器（Hub）")
			}
			need, err = a.runToolsHub(ctx, need.Tools)

		default:
			return session.EndReason{}, fmt.Errorf("未知需求种类 %d", need.Kind)
		}
		if err != nil {
			a.endStep("error")
			return session.EndReason{}, err
		}
	}
}

// beginStep / endStep：一个 step = 一次模型调用 + 它产生的工具调用（M4.7）。
func (a *Agent) beginStep(n uint32) {
	a.step = a.startSpan("step", "", a.TurnSpanID).Set("step", n)
	a.spanMu.Lock()
	a.stepID = a.step.s.ID
	a.spanMu.Unlock()
}

func (a *Agent) endStep(status string) {
	if a.step == nil {
		return
	}
	a.step.End(status)
	a.step = nil
}

func (a *Agent) curStep() string {
	a.spanMu.Lock()
	defer a.spanMu.Unlock()
	return a.stepID
}

// point 在一个 checkpoint 点提交；命中故障注入点时提交后返回 errCrash。
func (a *Agent) point(ctx context.Context, name, run string, rec *reqlog.Record) error {
	if err := a.checkpoint(ctx, run, rec); err != nil {
		return err
	}
	if a.crashAt == name {
		return errCrash
	}
	return nil
}

func (a *Agent) callModel(ctx context.Context, need *session.Need) (*session.Need, error) {
	var (
		req  session.Request
		view *reqlog.CompactionView
	)
	if a.recovered != nil { // 恢复：原样重发上次记录的请求（S7）
		req, a.recovered = *a.recovered, nil
		if a.recoveredView != nil {
			view, a.recoveredView = a.recoveredView, nil
		}
	} else {
		var err error
		if req, view, err = a.buildRequest(ctx, false); err != nil {
			return nil, err
		}
	}
	return a.sendModel(ctx, need, req, view, false)
}

// buildRequest 构造本步基础请求；配置了压缩器时先问它要不要压缩（最外层，先于 modelhub 全部层）。
// 压缩失败不挡模型调用（失败开放），只记 span；force 用于上下文超限后的强制压缩。
func (a *Agent) buildRequest(ctx context.Context, force bool) (session.Request, *reqlog.CompactionView, error) {
	cx := a.Core.Context()
	if a.Compactor != nil {
		sp := a.startSpan("compact", "", a.curStep()).Set("force", force)
		view, meta, err := a.Compactor.Plan(ctx, a.SessionID, a.Core, force)
		switch {
		case err != nil:
			sp.Set("error", err.Error()).End("error")
		case view == nil:
			sp.End("skip")
		default:
			if _, err := a.Core.Compact(view.Summary, view.Upto, meta); err != nil {
				sp.Set("error", err.Error()).End("error")
				break
			}
			req, err := session.BuildWithCompaction(cx, view.Summary, view.Upto)
			if err != nil {
				sp.Set("error", err.Error()).End("error")
				break
			}
			for k, v := range meta {
				sp.Set(k, v)
			}
			sp.Set("upto", view.Upto).End("ok")
			return req, view, nil
		}
	}
	req, err := session.BuildRequest(cx)
	return req, nil, err
}

func (a *Agent) sendModel(ctx context.Context, need *session.Need, req session.Request, view *reqlog.CompactionView, retried bool) (*session.Need, error) {
	// 压缩视图之后的最终请求 = 基础请求；Transform 层在 modelhub 内，R2 由调度器校验。
	rec, err := reqlog.Build(need.Model.RequestID, a.Core.Context(), view, req, req, nil)
	if err != nil {
		return nil, err
	}
	if err := a.point(ctx, "before_model", "running", rec); err != nil { // 模型调用前 checkpoint（H11）
		return nil, err
	}
	sp := a.startSpan("attempt", req.Model, a.curStep()).Set("request_id", need.Model.RequestID).
		Set("turn", need.Model.Turn).Set("step", need.Model.Step).Set("shell_hash", rec.FinalHash)
	first := time.Time{}
	onText := func(d string) {
		if first.IsZero() {
			first = a.clock()
		}
		if a.Hooks.OnText != nil {
			a.Hooks.OnText(d)
		}
	}
	var (
		reply session.AssistantItem
		stop  session.StopReason
	)
	if rm, ok := a.Model.(RequestModel); ok { // modelhub：带 need id（R3）与 Reset
		reply, stop, err = rm.StreamRequest(ctx, need.Model.RequestID, req, onText, a.Hooks.OnReset)
	} else {
		reply, stop, err = a.Model.Stream(ctx, req, onText)
	}
	if !first.IsZero() {
		sp.Set("ttft_ms", first.Sub(sp.s.Start).Milliseconds())
	}
	sp.Set("input", reply.Usage.Input).Set("output", reply.Usage.Output).
		Set("cache_read", reply.Usage.CacheRead).Set("cache_write", reply.Usage.CacheWrite).Set("usage_source", reply.Usage.Source).Set("stop", stop.V)
	status := "ok"
	if err != nil {
		status = "error"
		sp.Set("error", err.Error())
	}
	sp.End(status)
	// 上下文超限：强制压缩一次后重发（DSH maxOverflowRetries=1）。
	if err != nil && ctx.Err() == nil && !retried && a.Compactor != nil && modelhub.IsCode(err, modelhub.CodeContextOverflow) {
		if r2, v2, e := a.buildRequest(ctx, true); e == nil && v2 != nil {
			return a.sendModel(ctx, need, r2, v2, true)
		}
	}
	id, nid := need.Model.RequestID, need.ID
	switch {
	case err == nil:
		_, n, e := a.Core.ModelDone(id, nid, reply, stop)
		return n, e
	case ctx.Err() != nil:
		_, n, e := a.Core.ModelInterrupted(id, nid, reply, session.CancelledReason())
		return n, e
	default:
		_, n, e := a.Core.ModelFailed(id, nid, session.FailedReason(err.Error()))
		return n, e
	}
}

// History 返回会话可见消息（用于恢复后回显）。
func History(c *session.Core) []session.Message {
	req, err := session.BuildRequest(c.Context())
	if err != nil {
		return nil
	}
	return req.Messages
}

