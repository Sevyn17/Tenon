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
	"github.com/Sevyn17/tenon/runtime/session"
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
	// 起进程 + 握手在 Start 里做（要等进程起来）；工具点位在这里登记一个懒加载条目。
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
	if len(p.cfg.Command) == 0 {
		return nil // 没配命令：组件空转
	}
	p.sup = &Supervisor{Argv: p.cfg.Command, Dir: p.cfg.Dir, Env: p.cfg.Env,
		MaxCrashes: p.cfg.MaxCrashes,
		OnUnavailable: func() {
			if p.ui != nil {
				p.ui.Warn(ServiceName, "扩展进程连续崩溃，已标记不可用")
			}
		}}
	if err := p.sup.Start(sc.Context()); err != nil {
		return err
	}
	sc.OnClose(func(context.Context) error { return p.sup.Stop() })
	// 握手：报告能力与工具，越界拒绝。
	p.conn = extproc.NewConn(p.sup.Stdin(), p.sup.Stdout())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	raw, err := p.conn.Call(ctx, "handshake", map[string]any{"version": extproc.Version})
	if err != nil {
		return fmt.Errorf("extproc: 握手失败：%w", err)
	}
	var hs extproc.Handshake
	if err := json.Unmarshal(raw, &hs); err != nil {
		return fmt.Errorf("extproc: 握手响应不合法：%w", err)
	}
	if err := hs.Validate(); err != nil {
		_ = p.sup.Stop()
		return err // 越界能力：拒绝启动（硬边界）
	}
	p.tools = hs.ToolSpecs
	return nil
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
