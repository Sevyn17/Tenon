// Package extproc 是外部进程插件宿主：起一个扩展进程、握手、把握手报告的
// 能力（工具 / 中间件 / 事件 / 匹配器 / 资源类型）登记到本进程，
// 工具调用经 JSON-RPC 回桥转发（框架设计 04 4.8，T6b）。
package extproc

// 外部进程插件组件（框架设计 04 4.8，T6b）：起一个扩展进程、握手、把握手报告的工具
// 登记到本会话工具点位。工具调用经 JSON-RPC 回桥转发给进程。
//
// 只开 04 4.8 允许的能力（工具 / 中间件 / 事件订阅 / 匹配器 / 资源类型 / 渲染器）；
// 握手越界（guard / provide / model_call / context）直接拒绝启动。

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Sevyn17/tenon/runtime/assemble"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/extproc"
	"github.com/Sevyn17/tenon/runtime/mail"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// ServiceName 组件名。
const ServiceName = "extproc"

// CallTimeout 单次工具调用超时。
const CallTimeout = 60 * time.Second

// Config 组件配置（[plugin.extproc]）：要起的插件进程。
type Config struct {
	// Command 启动命令（argv），第一个是程序。
	Command []string `cfg:"command"`
	// Dir 工作目录（可空）。
	Dir string `cfg:"dir"`
	// Env 额外环境变量（可空）。
	Env []string `cfg:"env"`
	// MaxCrashes 连续崩溃多少次后标记不可用（0 = 5）。
	MaxCrashes int `cfg:"max_crashes"`
}

type plug struct {
	cfg  Config
	sup  *Supervisor
	conn *extproc.Conn
	ui   assemble.UI
	// tools 握手后登记的工具名与描述（模型面）。
	tools []extproc.ToolSpec
	// middleware 握手报告的中间件点位（tool_call / deliver）。
	middleware []string
	// events 握手报告的事件订阅名。
	events []string
	// matcherSpecs / resourceSpecs 握手报告的匹配器与资源类型声明。
	matcherSpecs  []extproc.MatcherSpec
	resourceSpecs []extproc.ResourceSpec
}

func init() { component.Register(ServiceName, func() component.Component { return &plug{} }) }

func (*plug) Meta() component.Meta {
	return component.Meta{Name: ServiceName, Version: "0.1.0", Effects: []string{component.EffectExec, component.EffectTools},
		Config: &Config{}, Requires: []string{"?" + assemble.ServiceUI}}
}

func (p *plug) Setup(cx *component.Registrar) error {
	if c, ok := cx.Config().(*Config); ok && c != nil {
		p.cfg = *c
	}
	if ui, ok, err := component.UseOptional[assemble.UI](cx, assemble.ServiceUI); err == nil && ok {
		p.ui = ui
	}
	// 起进程并握手（装配期）：拿到能力与点位清单，才能登记工具与事件订阅。
	// 没配命令时组件空转（不握手）。
	if len(p.cfg.Command) > 0 {
		if err := p.startAndHandshake(); err != nil {
			return err
		}
	}
	// 起进程 + 握手在 Start 里做（要等进程起来）；工具点位在这里登记一个懒加载条目。
	// 中间件：ToolCall 与 Deliver 两个点位（04 4.8 只开这两个）。中间件内部检查 p.conn。
	// 04 4.3.1：不再登记「会话层」，直接登记到 ToolCall 点位。
	if p.hasMiddleware("tool_call") {
		component.Wrap(cx, component.ToolCall, p.toolCallMw())
	}
	// 匹配器：经回桥把判断转发给进程（04 4.8 允许匹配器，模型判断类匹配器也走这条路）。
	for _, spec := range p.matcherSpecs {
		name := spec.Name
		if err := cx.Matcher(name, p.remoteMatcher(name)); err != nil {
			return err
		}
	}
	// 资源类型：登记类型并把 Render 转发给进程（渲染器是 ResourceType.Render，不是独立点位）。
	for _, spec := range p.resourceSpecs {
		name := spec.Name
		if err := cx.ResourceType(component.ResourceType{Name: name,
			Render: func(rec component.ResourceRecord) string { return p.remoteRender(name, rec) }}); err != nil {
			return err
		}
	}
	// Deliver 中间件（04 4.3 Deliver 点位）：经回桥把「一封信封要投进会话」交给插件决定放行/拦截/改写。
	if p.hasMiddleware("deliver") {
		component.Wrap(cx, component.Deliver, func(ctx context.Context, req component.DeliverReq,
			next func(context.Context, component.DeliverReq) (component.DeliverResp, error)) (component.DeliverResp, error) {
			if p.conn == nil {
				return next(ctx, req) // 不可用：放行给内核（fail_open 由调用方决定，这里不阻断投递）
			}
			cctx, cancel := context.WithTimeout(ctx, CallTimeout)
			defer cancel()
			// 把信封的要点交给插件；插件返回 deny 则拦截。
			envJSON, _ := json.Marshal(req.Envelope)
			raw, err := p.conn.Call(cctx, "deliver", map[string]any{
				"session_id": req.SessionID, "envelope": json.RawMessage(envJSON), "from_remote": req.FromRemote})
			if err != nil {
				return component.DeliverResp{}, err // fail_closed：投递失败
			}
			var res struct {
				Verdict string `json:"verdict,omitempty"`
				Reason  string `json:"reason,omitempty"`
			}
			if json.Unmarshal(raw, &res) == nil && res.Verdict == "deny" {
				return component.DeliverResp{Delivered: false}, nil // 拦截：不报错
			}
			return next(ctx, req)
		})
	}
	// 事件订阅：按握手报告的事件名逐个登记（04 4.8 只开事件订阅这一能力）。
	for _, name := range p.events {
		if err := cx.On(name, func(ctx context.Context, e store.ComponentEvent) error {
			if p.conn == nil {
				return nil
			}
			return p.conn.Notify("event", extproc.EventNotice{Name: e.Name, At: e.At, Data: e.Data})
		}); err != nil {
			return err
		}
	}
	component.RegisterAt(cx, assemble.SessionTools, ServiceName, assemble.ToolsEntry{Order: 95,
		Tools: func() []*toolhub.Tool {
			if p.conn == nil {
				return nil
			}
			out := make([]*toolhub.Tool, 0, len(p.tools))
			for _, spec := range p.tools {
				out = append(out, p.toolOf(spec))
			}
			return out
		}})
	return nil
}

func (p *plug) Start(sc *component.Scope) error {
	if p.sup == nil {
		return nil // 没配命令：组件空转
	}
	sc.OnClose(func(context.Context) error { return p.sup.Stop() })
	return nil
}

// startAndHandshake 起进程并握手：校验能力边界，记下工具 / 中间件 / 事件清单。
func (p *plug) startAndHandshake() error {
	p.sup = &Supervisor{Argv: p.cfg.Command, Dir: p.cfg.Dir, Env: p.cfg.Env,
		MaxCrashes: p.cfg.MaxCrashes,
		OnUnavailable: func() {
			if p.ui != nil {
				p.ui.Warn(ServiceName, "扩展进程连续崩溃，已标记不可用")
			}
		}}
	if err := p.sup.Start(context.Background()); err != nil {
		return err
	}
	p.conn = extproc.NewConn(p.sup.Stdin(), p.sup.Stdout())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	raw, err := p.conn.Call(ctx, "handshake", map[string]any{"version": extproc.Version})
	if err != nil {
		_ = p.sup.Stop()
		return fmt.Errorf("extproc: 握手失败：%w", err)
	}
	var hs extproc.Handshake
	if err := json.Unmarshal(raw, &hs); err != nil {
		_ = p.sup.Stop()
		return fmt.Errorf("extproc: 握手响应不合法：%w", err)
	}
	if err := hs.Validate(); err != nil {
		_ = p.sup.Stop()
		return err // 越界能力：拒绝启动（硬边界）
	}
	p.tools = hs.ToolSpecs
	p.middleware = hs.Middleware
	p.events = hs.Events
	p.matcherSpecs = hs.MatcherSpecs
	p.resourceSpecs = hs.ResourceSpecs
	return nil
}

// remoteMatcher 造一个把判断转发给进程的匹配器。失败按不匹配处理（不阻塞扇出）。
func (p *plug) remoteMatcher(name string) component.Matcher {
	return func(rec component.ResourceRecord, params map[string]string) (component.Match, error) {
		if p.conn == nil {
			return component.Match{}, nil // 不可用：按不匹配
		}
		ctx, cancel := context.WithTimeout(context.Background(), CallTimeout)
		defer cancel()
		raw, err := p.conn.Call(ctx, "matcher", extproc.MatchCall{
			Matcher: name, Record: toMatchRecord(rec), Params: params})
		if err != nil {
			return component.Match{}, nil // 回桥失败按不匹配
		}
		var res extproc.MatchResult
		if json.Unmarshal(raw, &res) != nil {
			return component.Match{}, nil
		}
		if !res.Matched {
			return component.Match{}, nil
		}
		return component.Match{Matched: true, Mode: mail.Mode(res.Mode)}, nil
	}
}

// remoteRender 把资源渲染转发给进程；回桥失败退回记录正文（不丢内容）。
func (p *plug) remoteRender(name string, rec component.ResourceRecord) string {
	if p.conn == nil {
		return rec.Body
	}
	ctx, cancel := context.WithTimeout(context.Background(), CallTimeout)
	defer cancel()
	raw, err := p.conn.Call(ctx, "render", extproc.RenderCall{Resource: name, Record: toMatchRecord(rec)})
	if err != nil {
		return rec.Body
	}
	var res extproc.RenderResult
	if json.Unmarshal(raw, &res) != nil || res.Text == "" {
		return rec.Body
	}
	return res.Text
}

// toMatchRecord 把内部资源记录转成线上形状。
func toMatchRecord(rec component.ResourceRecord) extproc.MatchRecord {
	return extproc.MatchRecord{Resource: rec.Resource, Seq: rec.Seq,
		From: rec.From.String(), Body: rec.Body, Fields: rec.Fields}
}

// hasMiddleware 握手是否报告了某中间件点位。
func (p *plug) hasMiddleware(point string) bool {
	for _, m := range p.middleware {
		if m == point {
			return true
		}
	}
	return false
}

// middlewareCall 造一个把调用转发给插件的层：插件返回 deny 则拒绝，否则放行（可改写参数）。
// toolCallMw 扩展进程的 tool_call 中间件（04 4.3.1 ToolCall 点位）：
// 经回桥把一次工具调用交给进程决定放行/拒绝/改写参数。
//
// 语义：进程 deny → 拒绝（fail_closed）；进程改 args → 用改写的；回桥失败 → 拒绝（fail_closed）；
// 进程不可用（conn 为 nil）→ 放行给下一环（组件本身没起来，不该阻断所有工具调用）。
func (p *plug) toolCallMw() component.ToolCallMw {
	return func(ctx context.Context, req component.ToolCallReq,
		next func(context.Context, component.ToolCallReq) (component.ToolCallResp, error)) (component.ToolCallResp, error) {
		if p.conn == nil {
			return next(ctx, req)
		}
		cctx, cancel := context.WithTimeout(ctx, CallTimeout)
		defer cancel()
		raw, err := p.conn.Call(cctx, "middleware", extproc.MiddlewareCall{
			Point: "tool_call", Tool: req.Tool, Args: req.Args, SessionID: req.SessionID})
		if err != nil {
			return component.ToolCallResp{Denied: true}, nil // fail_closed
		}
		var res extproc.MiddlewareResult
		if json.Unmarshal(raw, &res) == nil {
			if res.Verdict == "deny" {
				return component.ToolCallResp{Denied: true}, nil
			}
			if len(res.Args) > 0 {
				req.Args = res.Args
			}
		}
		return next(ctx, req)
	}
}

// toolOf 把一个外部工具包成模型面工具：调用经回桥转发给进程。
func (p *plug) toolOf(spec extproc.ToolSpec) *toolhub.Tool {
	name := spec.Name
	return &toolhub.Tool{
		Spec: session.ToolSpec{Name: name, Description: spec.Description, Schema: spec.Schema},
		Risk: toolhub.Write,
		Call: func(ctx context.Context, cx *toolhub.Cx, args json.RawMessage) (toolhub.Output, error) {
			if p.conn == nil {
				return toolhub.Failf("扩展进程不可用"), nil
			}
			cctx, cancel := context.WithTimeout(ctx, CallTimeout)
			defer cancel()
			raw, err := p.conn.Call(cctx, "tools/call", map[string]any{"name": name, "args": args})
			if err != nil {
				return toolhub.Failf("扩展进程调用失败：%v", err), nil
			}
			var out struct {
				Text  string `json:"text"`
				Error string `json:"error"`
			}
			if err := json.Unmarshal(raw, &out); err != nil {
				return toolhub.Failf("扩展进程返回不合法：%v", err), nil
			}
			if out.Error != "" {
				return toolhub.Failf("%s", out.Error), nil
			}
			return toolhub.Text(out.Text), nil
		},
	}
}
