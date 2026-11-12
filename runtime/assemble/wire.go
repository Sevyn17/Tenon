package assemble

// 共同装配核心（2026-11-04 审查 P1-51）：SDK（tenon.App.Start）与应用宿主（apphost）原先各写一份
// "收集点位 → 建会话构造器 → 接宿主参数 → 起后台循环"，已经分叉（SDK 不设并发上限 / 所属树策略 / 保留期），
// 会话构造器还藏在 appapi.New 的副作用里，后台派发可能在构造器接上之前就跑起来。两边都改走这里。

import (
	"context"
	"time"

	"github.com/Sevyn17/tenon/runtime/agent"
	"github.com/Sevyn17/tenon/runtime/agentdef"
	"github.com/Sevyn17/tenon/runtime/bus"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/host"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
	"github.com/Sevyn17/tenon/runtime/toolhub"
	"github.com/Sevyn17/tenon/runtime/useconf"
)

// BlobGrace 工件回收的宽限期：没有引用的文件超过这么久未被触碰才删（约束清单「存储」）。
const BlobGrace = 24 * time.Hour

// WireOptions Wire 的输入。Asm、Host、DB 必填。
type WireOptions struct {
	Asm         *component.Assembly
	Host        *host.Host
	DB          store.Store
	Blobs       toolhub.Artifacts
	Models      *Models
	Workdir     string
	Kernel      Kernel
	Definitions agentdef.Source
	// MaxSteps 新会话的步数上限；<0 = 用 Kernel.Policy。
	MaxSteps int
	// SleepAfter >0 时覆盖 Kernel.SleepAfter（嵌入方 / 测试显式给出）。
	SleepAfter time.Duration
	// Hooks 应用自己的会话钩子（可空）。
	Hooks HooksFunc
	// UseSource 租户层与用户层的使用配置来源（06 6.3）；nil = 没有租户/用户层。
	UseSource useconf.Source
}

// Wired Wire 的结果。
type Wired struct {
	Builder *Builder
	host    *host.Host
	db      store.Store
	gc      func(context.Context) (int, error) // 工件回收（工件存储实现了 store.BlobGC 时非空）
	ready   []ReadyFunc
	retain  time.Duration
}

// Wire 收集点位、建会话构造器，并把它和 [kernel] 的宿主参数接到 Host 上（Guards、AfterTurn、
// SleepAfter、Limit、TreePolicy、工件回收、New / Child）。不启动后台循环（见 Background）。
func Wire(o WireOptions) (*Wired, error) {
	col, err := Collect(o.Asm)
	if err != nil {
		return nil, err
	}
	h := o.Host
	if h.DB == nil {
		h.DB = o.DB
	}
	// 组件登记的守卫规则（04 4.2、08 D4）：投递 / 建会话等内核点位在 Host 里执行。
	h.Guards = o.Asm.GuardRules()
	// 投递中间件链（04 4.3 Deliver 点位）：只有登记过中间件才装，没登记时 Host 直接走内核投递。
	if o.Asm.HasMiddleware(component.PointDeliverMw) {
		h.SetDeliverChain(o.Asm.DeliverChain(h.DeliverKernel()))
	}
	// 新建会话中间件链（04 4.3 SessionOpen 点位）：最内层是空实现（真正的建会话由 Host 自己做）。
	if o.Asm.HasMiddleware(component.PointSessionOpenMw) {
		h.SessionOpenChain = o.Asm.SessionOpenChain(func(ctx context.Context, req component.SessionOpenReq) (component.SessionOpenResp, error) {
			return component.SessionOpenResp{}, nil
		})
	}
	// 回合中间件链（04 4.3 Turn 点位）：存构造器，调用时由 Host 注入本轮 kernel。
	if o.Asm.HasMiddleware(component.PointTurnMw) {
		h.TurnChain = o.Asm.TurnChain
	}
	h.Definitions = o.Definitions
	h.Hooks.AfterTurn = func(id string, end session.EndReason) {
		for _, f := range col.AfterTurn {
			f(id, end)
		}
	}
	k := o.Kernel
	h.SleepAfter = k.SleepAfter
	if o.SleepAfter > 0 {
		h.SleepAfter = o.SleepAfter
	}
	h.Limit = k.Concurrency
	h.TreePolicy, h.TreeRestarts = k.TreePolicy, k.TreeRestarts
	h.MaxHop = k.MaxHop
	// 工件文件回收（审查 P1-41）：没有引用、超过宽限期未触碰的文件删掉；由 Background 里的存储清理循环调用。
	var blobGC func(context.Context) (int, error)
	if gc, ok := o.Blobs.(store.BlobGC); ok {
		blobGC = func(ctx context.Context) (int, error) { return gc.GC(ctx, BlobGrace) }
	}
	maxSteps := o.MaxSteps
	rb := &Builder{DB: o.DB, Artifacts: o.Blobs, Models: o.Models.Hub, Model: o.Models.Model, Workdir: o.Workdir,
		Policy: func() session.Policy {
			pol := k.Policy
			if maxSteps >= 0 {
				pol.MaxSteps = uint32(maxSteps)
			}
			return pol
		},
		Tools:       col.Tools,
		Inits:       col.Inits,
		ToolCallMW:  o.Asm.ToolCallTransforms(),
		Blocks:      col.Blocks, // 系统提示词由 prompt 组件提供
		Items:       col.Items,
		Kernel:      &k,
		Compactor:   col.Compactor, // 自动压缩与超限后的强制压缩（审查 P0-1）
		ContextMW:   ctxMiddleware(o.Asm),
		ModelMW:     modelMiddleware(o.Asm),
		Definitions: o.Definitions,
		Statuses:    col.Statuses,
		Schemas:     o.Asm.AgentSchemas(),
		UseSource:   o.UseSource,
		Hooks:       o.Hooks,
		Guards:      h.Guards,
	}
	// 顶层会话带委派 / 领导工具；子会话与工作组成员按 child 构造（深度 1）。
	h.New = func(id, def string) (*agent.Agent, *session.Config, error) { return rb.Build(id, def, false) }
	h.Child = func(id, def string) (*agent.Agent, *session.Config, error) { return rb.Build(id, def, true) }
	return &Wired{Builder: rb, host: h, db: o.DB, gc: blobGC, ready: col.Ready, retain: k.Retain}, nil
}

// Background 启动后台循环：休眠会话的兜底扫描（SleepAfter>0）、存储清理、启动时派发一次发件箱
// （上次进程在提交之后、派发之前退出时留下的外发）。须在 Wire 之后调用：派发会打开会话，要用 Host.New。
// 返回停止函数。
func (w *Wired) Background() context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	if w.host.SleepAfter > 0 {
		go w.host.ScanLoop(ctx, w.host.SleepAfter)
	}
	// 存储清理（过期的观测与记账行、无引用的工件）：只碰存储，不经宿主（审查 P2 归属）。
	go store.Maintain(ctx, store.MaintainOptions{Store: w.db, GC: w.gc, Keep: w.retain,
		Notify: func(kind string, data map[string]any) {
			if w.host.Bus != nil {
				w.host.Bus.Publish(bus.Envelope{Kind: kind, Data: data})
			}
		}})
	go func() { _, _ = w.host.FlushOutbox(ctx) }()
	return cancel
}

// Ready 跑 ready 点位（构造器就绪后：如工作组补投会打开领导会话），返回各自的错误。
func (w *Wired) Ready(ctx context.Context) []error {
	var errs []error
	for _, f := range w.ready {
		if err := f(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// ctxMiddleware 把 04 4.3 的 Context 点位中间件适配成 agent.ContextMiddleware。
// agent 包不 import component（底层包不依赖装配层），所以在这里做适配。
// 没有登记中间件时返回 nil（agent 直接走内核组装，零开销）。
func ctxMiddleware(asm *component.Assembly) agent.ContextMiddleware {
	if !asm.HasMiddleware(component.PointContextMw) {
		return nil
	}
	return ctxMW{asm: asm}
}

type ctxMW struct{ asm *component.Assembly }

func (m ctxMW) Build(ctx context.Context, req *session.Request, kernel func(context.Context, *session.Request) (*session.Request, error)) (*session.Request, error) {
	// 4.3 限制 1：系统提示词只来自定义和框架，中间件改 System 无效。
	// 这里保存内核版 System，链跑完后还原。
	sys := req.System
	creq := component.ContextReq{Request: req, System: sys}
	chain := m.asm.ContextChain(func(ctx context.Context, r component.ContextReq) (component.ContextResp, error) {
		out, err := kernel(ctx, r.Request)
		if err != nil {
			return component.ContextResp{}, err
		}
		return component.ContextResp{Messages: out.Messages}, nil
	})
	resp, err := chain(ctx, creq)
	if err != nil {
		return nil, err
	}
	// 还原系统提示词（限制 1），只接受 Messages 的改动（限制 3）。
	req.System = sys
	if resp.Messages != nil {
		req.Messages = resp.Messages
	}
	return req, nil
}

// modelMiddleware 把 04 4.3 的 ModelCall 点位中间件适配成 agent.ModelCallMiddleware。
// 没有登记中间件时返回 nil（agent 直接发请求，零开销）。
func modelMiddleware(asm *component.Assembly) agent.ModelCallMiddleware {
	if !asm.HasMiddleware(component.PointModelCallMw) {
		return nil
	}
	return modelMW{asm: asm}
}

type modelMW struct{ asm *component.Assembly }

func (m modelMW) Call(ctx context.Context, req *session.Request,
	kernel func(context.Context, *session.Request) (session.AssistantItem, session.StopReason, error)) (session.AssistantItem, session.StopReason, error) {
	chain := m.asm.ModelCallChain(func(ctx context.Context, r component.ModelCallReq) (component.ModelCallResp, error) {
		reply, stop, err := kernel(ctx, r.Request)
		if err != nil {
			return component.ModelCallResp{}, err
		}
		return component.ModelCallResp{Reply: reply, Stop: stop}, nil
	})
	resp, err := chain(ctx, component.ModelCallReq{Request: req})
	if err != nil {
		return session.AssistantItem{}, session.StopReason{}, err
	}
	return resp.Reply, resp.Stop, nil
}
