// Package runtime 按登记的会话贡献构造会话 agent（thin-kernel-plan S2）。
//
// 会话构造（建 hub、登记工具、叠层、拼系统块）原来写在 cmd/ybase 的 mk() 里；
// 现在由这里统一完成，工具、层、系统块都以“每会话贡献函数”登记，
// 后续由插件管理器的 session_tools / 会话层 / system_block 点位填入。
package runtime

import (
	"fmt"

	"ybase-kernel/core/agent"
	"ybase-kernel/core/modelhub"
	"ybase-kernel/core/session"
	"ybase-kernel/core/store/sqlite"
	"ybase-kernel/core/toolhub"
)

// SessionCx 构造一个会话时贡献函数拿到的上下文。
type SessionCx struct {
	ID      string
	Workdir string
	Hub     *toolhub.Hub
	Agent   *agent.Agent
	// Child 子会话（子智能体 / 工作组成员）：不再带委派工具，限制深度为 1。
	Child bool
	// Values 同一会话内贡献函数之间共享的状态（如指令层与指令系统块共用一份已装载集合）。
	Values map[string]any
}

// ToolsFunc 每会话工具；LayerFunc 每会话工具层（nil 层跳过）；BlockFunc 每会话系统块（nil 跳过）。
type (
	ToolsFunc func(*SessionCx) ([]*toolhub.Tool, error)
	LayerFunc func(*SessionCx) ([]toolhub.Layer, error)
	BlockFunc func(*SessionCx) (*session.SystemBlock, error)
	HooksFunc func(*SessionCx) agent.Hooks
)

// Builder 会话构造器。Tools / Layers / Blocks 按登记顺序生效：工具顺序即暴露顺序，层由外到内。
type Builder struct {
	DB        *sqlite.DB
	Artifacts toolhub.Artifacts
	Models    *modelhub.Hub
	Model     string
	Compactor agent.Compactor // 可空
	Workdir   string
	// Policy 新会话策略（每次调用取一份）。
	Policy func() session.Policy
	// Kernel [kernel] 节（工具缺省超时 / 确认截止 / 工件阈值）；nil = 内核缺省。
	Kernel *Kernel

	Tools  []ToolsFunc
	Layers []LayerFunc
	Blocks []BlockFunc
	Hooks  HooksFunc // 可空
	// Items 权限项声明（装配级，见 PointPermItems）：每个会话的 hub 先登记注册表，再登记工具。
	Items []toolhub.Item
}

// Build 构造会话 id 的 agent 与新会话配置；child 见 SessionCx.Child。
func (b *Builder) Build(id string, child bool) (*agent.Agent, *session.Config, error) {
	a := &agent.Agent{Store: b.DB, SessionID: id,
		Model: &modelhub.Session{Hub: b.Models, SessionKey: "ybase-" + id}}
	if b.Compactor != nil {
		a.Compactor = b.Compactor
	}
	ho := toolhub.Options{Store: agent.ToolStore{DB: b.DB}, Artifacts: b.Artifacts, OnSpan: a.HubSpan}
	if b.Kernel != nil {
		th := b.Kernel.Thresholds
		ho.Thresholds, ho.ToolTimeout, ho.ConfirmDeadline = &th, b.Kernel.ToolTimeout, b.Kernel.ConfirmDeadline
	}
	hub := toolhub.New(ho)
	cx := &SessionCx{ID: id, Workdir: b.Workdir, Hub: hub, Agent: a, Child: child, Values: map[string]any{}}
	for _, it := range b.Items {
		if err := hub.RegisterItem(it); err != nil {
			return nil, nil, err
		}
	}
	for _, f := range b.Tools {
		ts, err := f(cx)
		if err != nil {
			return nil, nil, err
		}
		for _, t := range ts {
			if err := hub.Register(t); err != nil {
				return nil, nil, fmt.Errorf("runtime: 登记工具 %s：%w", t.Spec.Name, err)
			}
		}
	}
	for _, f := range b.Layers {
		ls, err := f(cx)
		if err != nil {
			return nil, nil, err
		}
		for _, l := range ls {
			hub.AddLayer(l)
		}
	}
	if err := hub.Validate(); err != nil {
		return nil, nil, err
	}
	ts := hub.SpecsFor(nil, 0)
	a.Hub, a.Toolset = hub, ts
	if b.Hooks != nil {
		a.Hooks = b.Hooks(cx)
	}
	pol := session.DefaultPolicy()
	if b.Kernel != nil {
		pol = b.Kernel.Policy
	}
	if b.Policy != nil {
		pol = b.Policy()
	}
	cfg := &session.Config{Model: b.Model, Tools: hub.Exposed(ts), Policy: pol}
	for _, f := range b.Blocks {
		blk, err := f(cx)
		if err != nil {
			return nil, nil, err
		}
		if blk != nil {
			cfg.System = append(cfg.System, *blk)
		}
	}
	return a, cfg, nil
}
