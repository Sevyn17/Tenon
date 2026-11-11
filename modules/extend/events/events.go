package events

// 定时与外部事件入口（框架设计 13 13.4「事件」、09 9.8，T5b）。
//
// 统一入口把定时触发与外部事件转成信封：origin = external（由入口类型决定，组件不能自己声明）、
// mode = wake、from = component:events。目标二选一：agent:<名字>（不带 key，每次新建）或
// resident:<名字>（T5a 固定会话；这里归一成 agent: 交给常驻查找）。
//
// Router 分发：本组件登记一个会话工具 event_dispatch，让 Router 定义的智能体在一个回合里
// 把事件转发给 Worker（只用定义 + 配置拼出 Router/Worker，不写业务 Go）。
//
// 定时：组件配置 [plugin.events] 里声明任务，Start 里用进程内定时器按周期 Post；作用域结束即停。

import (
	"context"
	"strings"
	"time"

	"github.com/Sevyn17/tenon/runtime/assemble"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/host"
	"github.com/Sevyn17/tenon/runtime/mail"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// ServiceName 组件名（[plugin.events] 与组件登记名）。
const ServiceName = "events"

// Config 组件配置（[plugin.events]）：定时任务清单。
type Config struct {
	Schedule []Task `cfg:"schedule"`
}

// Task 一个定时任务。
type Task struct {
	Every  string `cfg:"every"`  // 周期，如 "24h"、"30s"
	Target string `cfg:"target"` // 目标地址，如 "agent:日报" 或 "resident:日报"
	Body   string `cfg:"body"`   // 正文
}

// Hub 外部事件入口。Post 把外部事件转成 origin=external/wake 的信封投递。
type Hub struct {
	h *host.Host
}

// Post 投一条外部事件给 target（agent:<名> / resident:<名> / session:<id>）。
func (h *Hub) Post(ctx context.Context, target, body string) error {
	addr, err := mail.ParseAddress(strings.TrimSpace(target))
	if err != nil {
		return err
	}
	env := mail.Envelope{
		From:   mail.Address{Kind: mail.AddrComponent, ID: ServiceName},
		Via:    ServiceName,
		To:     addr,
		Kind:   mail.KindRequest,
		Mode:   mail.ModeWake,
		Origin: mail.OriginExternal,
		Body:   body,
	}
	return h.h.Deliver(ctx, env)
}

type plug struct {
	hub *Hub
	cfg Config
}

func init() { component.Register(ServiceName, func() component.Component { return &plug{} }) }

func (*plug) Meta() component.Meta {
	return component.Meta{Name: ServiceName, Version: "0.1.0", Effects: []string{component.EffectTools},
		Requires: []string{assemble.ServiceHost}, Config: &Config{}}
}

func (p *plug) Setup(cx *component.Registrar) error {
	h, err := component.Use[*host.Host](cx, assemble.ServiceHost)
	if err != nil {
		return err
	}
	p.hub = &Hub{h: h}
	if c, ok := cx.Config().(*Config); ok && c != nil {
		p.cfg = *c
	}
	t := dispatchTool(p.hub)
	for _, it := range Items() {
		component.RegisterAt(cx, assemble.PermItems, it.ID, it)
	}
	component.RegisterAt(cx, assemble.SessionTools, ServiceName, assemble.ToolsEntry{Order: 60,
		Tools: func() []*toolhub.Tool { return []*toolhub.Tool{t} }})
	return nil
}

func (p *plug) Start(sc *component.Scope) error {
	for _, task := range p.cfg.Schedule {
		d, err := time.ParseDuration(task.Every)
		if err != nil || d <= 0 {
			continue // 配置错误：跳过该任务
		}
		task := task
		sc.Go("events:"+task.Target, func(ctx context.Context) error {
			return runTask(ctx, d, p.hub, task)
		})
	}
	return nil
}

// runTask 按周期投递，直到 ctx 取消。
func runTask(ctx context.Context, every time.Duration, hub *Hub, task Task) error {
	tk := time.NewTicker(every)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tk.C:
			_ = hub.Post(context.Background(), task.Target, task.Body)
		}
	}
}
