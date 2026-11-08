// Package assemble 按登记的会话贡献构造会话 agent（thin-kernel-plan S2）。
//
// 会话构造（建 hub、登记工具、叠层、拼系统块）原来写在 cmd/tenon 的 mk() 里；
// 现在由这里统一完成，工具、层、系统块都以“每会话贡献函数”登记，
// 后续由插件管理器的 session_tools / 会话层 / system_block 点位填入。
package assemble

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path"
	"strings"

	"github.com/Sevyn17/tenon/runtime/agent"
	"github.com/Sevyn17/tenon/runtime/agentdef"
	"github.com/Sevyn17/tenon/runtime/callctx"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/inject"
	"github.com/Sevyn17/tenon/runtime/modelhub"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
	"github.com/Sevyn17/tenon/runtime/toolhub"
	"github.com/Sevyn17/tenon/runtime/useconf"
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
	// Parent 子会话的父会话限制（顶层会话为 nil）：子会话不越过父会话——父定义的 tools 白名单
	// 收窄子会话的可用工具，父会话的使用配置（如 [use.permission] 上限档位）由各组件叠加到子会话上
	// （2026-11-04 审查 P2：原先子会话用内置定义，父会话的限制一样都不继承）。
	Parent *ParentLimits
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
	// Guards 组件登记的守卫规则；GuardToolCall 上的规则装成每个会话工具栈里的一层守卫（toolGuard）。
	Guards []component.GuardRule
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
		Model: &modelhub.Session{Hub: b.Models, SessionKey: "tenon-" + id}}
	a.Def = def
	a.Statuses = b.Statuses
	a.Schemas = b.Schemas
	if b.Compactor != nil {
		a.Compactor = b.Compactor
	}
	ho := toolhub.Options{Store: agent.ToolStore{DB: b.DB}, Artifacts: b.Artifacts, OnSpan: a.HubSpan, OnProgress: a.HubProgress}
	if b.Kernel != nil {
		th := b.Kernel.Thresholds
		ho.Thresholds, ho.ToolTimeout, ho.ConfirmDeadline = &th, b.Kernel.ToolTimeout, b.Kernel.ConfirmDeadline
	}
	hub := toolhub.New(ho)
	if ms, ok := a.Model.(*modelhub.Session); ok && b.Artifacts != nil {
		ms.Media = hub.ReadArtifact // 工具结果里的图片按工件引用取回，发给支持图片的模型
	}
	cx := &SessionCx{ID: id, Workdir: b.Workdir, Hub: hub, Agent: a, Child: child, Values: map[string]any{}, Ctx: kctx, Def: def}
	if child {
		if cx.Parent, err = b.parentLimits(id); err != nil {
			return nil, nil, err
		}
	}
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
	if l, ok := toolGuard(b.Guards); ok { // 组件守卫先于授权层判定：被拒的调用不再要人确认
		hub.PrependLayer(l)
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
	if cx.Parent != nil && cx.Parent.Def != nil {
		pa, err := defTools(cx.Parent.Def, hub.Names())
		if err != nil {
			return nil, nil, err
		}
		allow = intersectAllow(allow, pa)
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

// ParentLimits 子会话的父会话限制（见 SessionCx.Parent）。
type ParentLimits struct {
	ID  string
	Def *agentdef.Agent // 父会话的定义；nil = 父会话没有定义（不收窄工具）
	// Use 父会话算好的使用配置（会话行 use_config）：组件名 → 配置。
	Use map[string]map[string]any
}

// UseValue 父会话给某组件某项的使用配置：先看算好的 use_config，没有再看父定义的 [use.*]。
func (p *ParentLimits) UseValue(component, key string) (any, bool) {
	if p == nil {
		return nil, false
	}
	if v, ok := p.Use[component][key]; ok {
		return v, true
	}
	if p.Def != nil {
		v, ok := p.Def.Use[component][key]
		return v, ok
	}
	return nil, false
}

// parentLimits 读子会话 id 的父会话（会话行有 ParentID 就用它；新建时会话行还不存在，按 id 约定
// "<父 id>/<子名>" 取，subagent 与 workgroup 都这样拼）。父会话不存在返回 nil。
func (b *Builder) parentLimits(id string) (*ParentLimits, error) {
	if b.DB == nil {
		return nil, nil
	}
	ctx := context.Background()
	noEntries := math.MaxInt
	parent := ""
	if l, err := b.DB.Load(ctx, id, &noEntries); err == nil {
		parent = l.Meta.ParentID
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("runtime: 读子会话 %s：%w", id, err)
	}
	if parent == "" {
		i := strings.LastIndex(id, "/")
		if i <= 0 {
			return nil, nil
		}
		parent = id[:i]
	}
	l, err := b.DB.Load(ctx, parent, &noEntries)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("runtime: 读父会话 %s：%w", parent, err)
	}
	p := &ParentLimits{ID: parent}
	if len(l.Meta.UseConfig) > 0 {
		if err := json.Unmarshal(l.Meta.UseConfig, &p.Use); err != nil {
			return nil, fmt.Errorf("runtime: 父会话 %s 的 use_config：%w", parent, err)
		}
	}
	if l.Meta.AgentID != "" {
		if p.Def, err = b.loadDefinition(parent, l.Meta.AgentID); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// intersectAllow 两个工具白名单的交集；nil = 不限。
func intersectAllow(a, b []string) []string {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	keep := make(map[string]bool, len(b))
	for _, n := range b {
		keep[n] = true
	}
	out := []string{}
	for _, n := range a {
		if keep[n] {
			out = append(out, n)
		}
	}
	return out
}

// toolGuard 把 GuardToolCall 上的守卫规则做成一层工具守卫；没有这类规则时不装。
// Deny 返回失败结果给模型（不中止回合）；Allow / Abstain 都交给下一层（授权仍由权限组件判定）。
func toolGuard(rules []component.GuardRule) (toolhub.Layer, bool) {
	var own []component.GuardRule
	for _, r := range rules {
		if r.On == component.GuardToolCall && r.Check != nil {
			own = append(own, r)
		}
	}
	if len(own) == 0 {
		return toolhub.Layer{}, false
	}
	return toolhub.Layer{Name: "component-guard", Guard: true,
		Call: func(ctx context.Context, cx *toolhub.Cx, args json.RawMessage, next toolhub.Next) (toolhub.Output, error) {
			req := component.GuardRequest{Point: component.GuardToolCall, Args: args}
			if cx.Tool != nil {
				req.Tool = cx.Tool.Spec.Name
				if cx.Tool.Risk != toolhub.Read {
					req.Risk = component.RiskHigh
				}
			}
			if v, why := component.EvalGuards(own, req); v == component.Deny {
				return toolhub.Failf("工具 %s 被守卫拒绝：%s", req.Tool, why), nil
			}
			return next(ctx, cx, args)
		}}, true
}
