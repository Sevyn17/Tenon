// Package runtime 按登记的会话贡献构造会话 agent（thin-kernel-plan S2）。
//
// 会话构造（建 hub、登记工具、叠层、拼系统块）原来写在 cmd/ybase 的 mk() 里；
// 现在由这里统一完成，工具、层、系统块都以“每会话贡献函数”登记，
// 后续由插件管理器的 session_tools / 会话层 / system_block 点位填入。
package assemble

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"ybase/runtime/agent"
	"ybase/runtime/agentdef"
	"ybase/runtime/callctx"
	"ybase/runtime/inject"
	"ybase/runtime/modelhub"
	"ybase/runtime/session"
	"ybase/runtime/store"
	"ybase/runtime/toolhub"
	"ybase/runtime/useconf"
)

// SessionCx 构造一个会话时贡献函数拿到的上下文。
type SessionCx struct {
	ID      string
	Workdir string
	Hub     *toolhub.Hub
	Agent   *agent.Agent
	// Ctx 本次会话的调用上下文（工具与层在调用时通过 toolhub.Cx.Ctx 拿到它）。
	Ctx *callctx.Ctx
	// Child 子会话（子智能体 / 工作组成员）：不再带委派工具，限制深度为 1。
	Child bool
	// Def 本会话的智能体定义（T1b-2c）；nil = 无定义会话（用户直接新建的顶层会话）。
	Def *agentdef.Agent
	// Values 同一会话内贡献函数之间共享的状态（如指令层与指令系统块共用一份已装载集合）。
	Values map[string]any
}

// ToolSet 装配期登记好的一组工具；LayerFunc 每会话工具层（nil 层跳过）；BlockFunc 每会话系统块（nil 跳过）。
type (
	ToolSet struct {
		Tools   func() []*toolhub.Tool
		Visible func(*SessionCx) bool // 可空 = 所有会话都要
	}
	LayerFunc func(*SessionCx) ([]toolhub.Layer, error)
	BlockFunc func(*SessionCx) (*session.SystemBlock, error)
	HooksFunc func(*SessionCx) agent.Hooks
)

// Builder 会话构造器。Tools / Layers / Blocks 按登记顺序生效：工具顺序即暴露顺序，层由外到内。
type Builder struct {
	DB        store.Store
	Artifacts toolhub.Artifacts
	Models    *modelhub.Hub
	Model     string
	Compactor agent.Compactor // 可空
	Workdir   string
	// Policy 新会话策略（每次调用取一份）。
	Policy func() session.Policy
	// Kernel [kernel] 节（工具缺省超时 / 确认截止 / 工件阈值）；nil = 内核缺省。
	Kernel *Kernel

	Tools  []ToolSet
	Layers []LayerFunc
	Blocks []BlockFunc
	Hooks  HooksFunc // 可空
	// Items 权限项声明（装配级，见 PointPermItems）：每个会话的 hub 先登记注册表，再登记工具。
	Items []toolhub.Item
	// Definitions 智能体定义来源（03 3.5）；nil = 会话不带定义（也没有可加载的定义名）。
	Definitions agentdef.Source
	// Statuses 状态提交登记（框架设计 12.5）：每次调模型前由 agent 收集变化的片段。
	Statuses []inject.Reg
	// Schemas 装配期各组件登记的使用配置原型（06 6.2，Assembly.AgentSchemas）：新会话按定义的
	// [use.*] 算 use_config 用；nil = 定义里写了 [use.*] 的会话创建失败（没有组件认这份配置）。
	Schemas []useconf.Schema
}

// modelFor 会话用的模型：定义写了 model 就用它（经目录解析校验，写错即报错），否则用全局缺省。
// 原先一律用全局模型，定义里的 model 不生效（2026-11-04 审查 P0-3）。
func (b *Builder) modelFor(def *agentdef.Agent) (string, error) {
	if def == nil || def.Model == "" {
		return b.Model, nil
	}
	if b.Models != nil {
		if _, err := b.Models.Catalog().Resolve(def.Model); err != nil {
			return "", fmt.Errorf("runtime: 定义 %s 的模型 %q：%w", def.ID, def.Model, err)
		}
	}
	return def.Model, nil
}

// Build 构造会话 id 的 agent 与新会话配置。
//
// defName 是要用的定义名（空 = 无定义；恢复已有会话时从会话行 meta 里取，见 loadDefinition），
// child 见 SessionCx.Child。
func (b *Builder) Build(id, defName string, child bool) (*agent.Agent, *session.Config, error) {
	def, err := b.loadDefinition(id, defName)
	if err != nil {
		return nil, nil, err
	}
	// 调用上下文：会话身份在 agent.Open 时由会话行填全（runtime/agent/ctx.go）。
	kctx := callctx.New(callctx.Options{Tenant: store.Tenant, Workdir: b.Workdir, SessionID: id, Child: child})
	a := &agent.Agent{Store: b.DB, SessionID: id, Ctx: kctx,
		Model: &modelhub.Session{Hub: b.Models, SessionKey: "ybase-" + id}}
	a.Def = def
	a.Statuses = b.Statuses
	a.Schemas = b.Schemas
	if b.Compactor != nil {
		a.Compactor = b.Compactor
	}
	ho := toolhub.Options{Store: agent.ToolStore{DB: b.DB}, Artifacts: b.Artifacts, OnSpan: a.HubSpan}
	if b.Kernel != nil {
		th := b.Kernel.Thresholds
		ho.Thresholds, ho.ToolTimeout, ho.ConfirmDeadline = &th, b.Kernel.ToolTimeout, b.Kernel.ConfirmDeadline
	}
	hub := toolhub.New(ho)
	cx := &SessionCx{ID: id, Workdir: b.Workdir, Hub: hub, Agent: a, Child: child, Values: map[string]any{}, Ctx: kctx, Def: def}
	for _, it := range b.Items {
		if err := hub.RegisterItem(it); err != nil {
			return nil, nil, err
		}
	}
	for _, set := range b.Tools {
		if set.Visible != nil && !set.Visible(cx) {
			continue
		}
		if set.Tools == nil {
			continue
		}
		for _, t := range set.Tools() {
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
	if !hub.Authorized() { // 没装权限组件：用内核缺省授权层（有副作用的工具一律询问，P0-7）
		hub.AddLayer(toolhub.DefaultAuthorizer())
	}
	if err := hub.Validate(); err != nil {
		return nil, nil, err
	}
	allow, err := defTools(def, hub.Names())
	if err != nil {
		return nil, nil, err
	}
	ts := hub.SpecsFor(allow, 0)
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
	model, err := b.modelFor(def)
	if err != nil {
		return nil, nil, err
	}
	cfg := &session.Config{Model: model, Tools: hub.Exposed(ts), Policy: pol}
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

// defTools 把定义的 tools 白名单（支持 `*` / `?` 通配，03 3.2）展开成本会话的可用工具集合
// （08 8.3：有效权限 = tools ∩ [use.permission] ∩ caps …，这里是第一项）。
// 定义为 nil 或没写 tools 时返回 nil = 不收窄。写了却一项都没匹配到任何已登记工具时报错——
// 多半是拼错了名字，悄悄给一个空工具集只会让问题更难查。
func defTools(def *agentdef.Agent, registered []string) ([]string, error) {
	if def == nil || len(def.Tools) == 0 {
		return nil, nil
	}
	out := []string{}
	seen := map[string]bool{}
	var unmatched []string
	for _, pat := range def.Tools {
		hit := false
		for _, n := range registered {
			ok, err := path.Match(pat, n)
			if err != nil {
				return nil, fmt.Errorf("runtime: 定义 %s 的 tools 项 %q 不合法：%w", def.ID, pat, err)
			}
			if ok {
				hit = true
				if !seen[n] {
					seen[n] = true
					out = append(out, n)
				}
			}
		}
		if !hit {
			unmatched = append(unmatched, pat)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("runtime: 定义 %s 的 tools 没有匹配到任何已登记工具：%s", def.ID, strings.Join(unmatched, "、"))
	}
	return out, nil
}

// loadDefinition 取会话要用的智能体定义（T1b-2c）。
//
//   - 显式给了名字（子智能体 / 工作组成员由宿主按内置模板名传入）就用它；
//   - 名字为空且会话已存在时用会话行里记的定义名（重开会话还是同一个角色）；
//   - 都没有 → 无定义会话，返回 nil。
func (b *Builder) loadDefinition(id, name string) (*agentdef.Agent, error) {
	if b.Definitions == nil {
		if name == "" {
			return nil, nil
		}
		return nil, fmt.Errorf("runtime: 未配置定义来源，无法加载定义 %s", name)
	}
	if name == "" {
		if b.DB == nil {
			return nil, nil
		}
		l, err := b.DB.Load(context.Background(), id, nil)
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("runtime: 读会话 %s 的定义：%w", id, err)
		}
		if name = l.Meta.AgentID; name == "" {
			return nil, nil
		}
	}
	a, _, err := agentdef.Resolve(context.Background(), b.Definitions, agentdef.Scope{Tenant: store.Tenant}, name)
	if err != nil {
		return nil, fmt.Errorf("runtime: 会话 %s 的定义 %s：%w", id, name, err)
	}
	return &a, nil
}
