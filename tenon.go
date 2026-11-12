package tenon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Sevyn17/tenon/adapters"
	"github.com/Sevyn17/tenon/appapi"
	"github.com/Sevyn17/tenon/runtime/agent"
	"github.com/Sevyn17/tenon/runtime/agentdef"
	"github.com/Sevyn17/tenon/runtime/assemble"
	"github.com/Sevyn17/tenon/runtime/bus"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/host"
	"github.com/Sevyn17/tenon/runtime/pkgs"
	"github.com/Sevyn17/tenon/runtime/store"
	"github.com/Sevyn17/tenon/runtime/toolhub"
	"github.com/Sevyn17/tenon/runtime/useconf"
)

// Component 应用自带的组件（04 4.2：Name + Setup），需要后台工作的实现 Start。
// 内置组件由预设提供，应用一般只在 With 里加自己的组件。要声明配置、提供或使用服务的，
// 再实现 Info() ComponentInfo（见 components.go）。
type Component interface {
	Name() string
	Setup(cx *Registrar) error
	Start(sc *Scope) error
}

// componentPlugin 把 Component 适配成 component.Component（装配输入用）。
type componentPlugin struct{ c Component }

func (p componentPlugin) Meta() component.Meta {
	m := component.Meta{Name: p.c.Name(), Default: true}
	if d, ok := p.c.(interface{ Info() ComponentInfo }); ok {
		in := d.Info()
		m.Config, m.Provides, m.Requires = in.Config, in.Provides, in.Requires
	}
	return m
}

func (p componentPlugin) Setup(cx *component.Registrar) error { return p.c.Setup(cx) }

func (p componentPlugin) Start(sc *component.Scope) error { return p.c.Start(sc) }

// Option New 的选项。
type Option func(*config) error

// config New 的装配输入。
type config struct {
	components []string          // 预设组件名（按顺序）
	extra      []Component       // With 加进来的组件
	without    []string          // Without 关掉的组件
	store      store.Store       // WithStore
	models     *assemble.Models  // WithModels
	agents     []agentdef.Source // WithAgents
	// packages 插件包（WithPackages）：包带的定义单独存放，不算应用定义（T4d 审查 H2）。
	packages []*pkgs.Package
	workdir  string
	ui       assemble.UI
	// protocolUI 界面经应用协议（WithProtocolUI）。
	protocolUI bool
	// configs 组件配置（WithConfig），按组件名合并。
	configs map[string]map[string]any
	// sleepAfter 空闲会话休眠超时（0 = 不休眠，02 2.2）；>0 时启动兜底扫描。
	sleepAfter time.Duration
	// projectTrust 工作目录是否受信（WithProjectTrust）；缺省 false。
	projectTrust bool
	// kernel 内核与宿主参数（WithKernel；键同 profile 的 [kernel] 节）。
	kernel map[string]any
	// 应用宿主用的（host_options.go）。
	profile   *component.Profile
	services  map[string]any
	workspace string
	hooks     func(*assemble.SessionCx) agent.Hooks
	approvals adapters.ModeApprovals
	protocol  []func(*appapi.Options)
	readyErrs func([]error)
	// 外观（tools.go、agents.go）。
	tools        []Tool
	inline       []Agent
	defaultAgent string
	secrets      func(string) (string, error) // WithSecrets
	// useSource 租户/用户层使用配置来源（WithConfigSource，06 6.3）。
	useSource useconf.Source
	// layers 部署期配置的分层来源（WithLayeredConfig，18 方案第四节）。
	layers component.LayeredConfig
}

// Default 标准组件预设（10 10.2）：内核、上下文、模型、工具、权限、沙箱、协作。
// 不含存储：存储由 WithStore 提供。
func Default() Option {
	return func(c *config) error {
		c.components = append(c.components, defaultComponents...)
		return nil
	}
}

// Minimal 最小预设（10 10.2）：内核 + 上下文（含压缩所需的模型组件）。
func Minimal() Option {
	return func(c *config) error {
		c.components = append(c.components, minimalComponents...)
		return nil
	}
}

// With 加应用自己的组件。
func With(cs ...Component) Option {
	return func(c *config) error {
		for _, comp := range cs {
			if comp == nil {
				return fmt.Errorf("%w: With 里有 nil 组件", ErrInvalid)
			}
			if strings.TrimSpace(comp.Name()) == "" {
				return fmt.Errorf("%w: 组件名不能为空", ErrInvalid)
			}
			c.extra = append(c.extra, comp)
		}
		return nil
	}
}

// Without 关掉预设里的组件（名字必须是预设里的）。
func Without(names ...string) Option {
	return func(c *config) error {
		for _, n := range names {
			if n == "" {
				return fmt.Errorf("%w: Without 里组件名不能为空", ErrInvalid)
			}
			c.without = append(c.without, n)
		}
		return nil
	}
}

// WithStore 存储（必需；预设不含存储）。
func WithStore(s Store) Option {
	return func(c *config) error {
		if s == nil {
			return fmt.Errorf("%w: WithStore 传了 nil", ErrInvalid)
		}
		c.store = s
		return nil
	}
}

// WithModels 用现成的模型来源替换预设里的 models + 适配器（测试、嵌入方自带模型时用）。
func WithModels(hub *ModelHub, model string) Option {
	return func(c *config) error {
		if hub == nil {
			return fmt.Errorf("%w: WithModels 的 hub 为 nil", ErrInvalid)
		}
		c.models = &assemble.Models{Hub: hub, Model: model}
		return nil
	}
}

// WithAgents 追加定义来源（可多个；平台层由组件模板补齐）。
func WithAgents(srcs ...AgentSource) Option {
	return func(c *config) error {
		for _, s := range srcs {
			if s == nil {
				return fmt.Errorf("%w: WithAgents 里有 nil 来源", ErrInvalid)
			}
			c.agents = append(c.agents, s)
		}
		return nil
	}
}

// Dir 目录定义来源（agentdef.DirSource）：root/platform/<名>、root/tenant/<租户>/<名>、
// root/user/<租户>/<用户>/<名>。读盘由调用方这一侧完成（os.DirFS），内核不自己读。
func Dir(root string) AgentSource { return agentdef.FS(os.DirFS(root), root) }

// WithWorkdir 会话工作目录（fs / shell / instructions 等组件要用）。
func WithWorkdir(dir string) Option {
	return func(c *config) error {
		c.workdir = dir
		return nil
	}
}

// WithUI 界面输出（缺省丢弃输出）。
func WithUI(ui UI) Option {
	return func(c *config) error {
		if ui == nil {
			return fmt.Errorf("%w: WithUI 传了 nil", ErrInvalid)
		}
		c.ui = ui
		return nil
	}
}

// WithConfig 设一个预设组件的配置键，等同 profile 里的 [plugin.<名>] 节（如 models 的 base_url、model）。
// 多次调用按键合并、后者覆盖前者；组件不在装配里、键未知或类型不对在 New 里报错。
func WithConfig(component string, kv map[string]any) Option {
	return func(c *config) error {
		if component == "" {
			return fmt.Errorf("%w: WithConfig 的组件名为空", ErrInvalid)
		}
		if c.configs == nil {
			c.configs = map[string]map[string]any{}
		}
		if c.configs[component] == nil {
			c.configs[component] = map[string]any{}
		}
		for k, v := range kv {
			c.configs[component][k] = v
		}
		return nil
	}
}

// WithProtocolUI 界面经应用协议：插件输出发成 notice、向人提问登记成交互，由连在 App.API() 上的客户端
// （框架的前端或别的协议客户端）回答。和 WithUI 二选一；都不给时输出丢弃、提问直接报错。
func WithProtocolUI() Option {
	return func(c *config) error {
		c.protocolUI = true
		return nil
	}
}

// WithSleepAfter 空闲会话的休眠超时（02 2.2）：>0 时空闲会话释放对象与租约，
// 有新信件时唤醒；同时启动兜底扫描。0（缺省）= 不休眠。
func WithSleepAfter(d time.Duration) Option {
	return func(c *config) error {
		if d < 0 {
			return fmt.Errorf("%w: WithSleepAfter 不能为负", ErrInvalid)
		}
		c.sleepAfter = d
		return nil
	}
}

// WithProjectTrust 声明工作目录受信：受信时才读其中的 .tenon/hooks.toml、mcp.toml、项目权限档位、
// AGENTS.md 与项目技能。缺省不受信——嵌入方指向任意目录时不执行目录里的钩子与 MCP 命令
// （2026-11-04 审查 P1-5：原先写死受信）。
func WithProjectTrust(trusted bool) Option {
	return func(c *config) error {
		c.projectTrust = trusted
		return nil
	}
}

// WithKernel 内核与宿主参数，键与 profile 的 [kernel] 节相同：max_steps、tool_timeout、confirm_deadline、
// sleep_after、concurrency、tree_policy、tree_restarts、retain 等。未知键或非法值在 New 里报 ErrInvalid。
// 多次调用按键合并，后者覆盖前者（2026-11-04 审查 P1-51：原先 SDK 设不了并发上限、所属树策略与保留期）。
func WithKernel(kv map[string]any) Option {
	return func(c *config) error {
		if c.kernel == nil {
			c.kernel = map[string]any{}
		}
		for k, v := range kv {
			c.kernel[k] = v
		}
		return nil
	}
}

// WithLayeredConfig 提供部署期配置的分层来源（18 方案第四节）：hooks、permission、mcp
// 从这里按层取配置，不再自己读文件。nil（缺省）= 没有外部配置，组件只用代码里给的值。
// 与 WithConfigSource 不是一回事：那个是会话级 [use.*] 的租户 / 用户层。
func WithLayeredConfig(src component.LayeredConfig) Option {
	return func(c *config) error {
		c.layers = src
		return nil
	}
}

// WithConfigSource 提供租户层与用户层的使用配置来源（06 6.3）：组件按 [use.<组件>] 的
// tenant 标签只允许覆盖这些字段；limit:"min"/"intersect" 各层取严。nil = 没有租户/用户层。
// 会话打开时按创建者算一次；守卫每轮再按本轮触发者的上限取一次（08 8.3，依赖本来源）。
func WithConfigSource(src useconf.Source) Option {
	return func(c *config) error {
		c.useSource = src
		return nil
	}
}

// New 装配应用：按组件清单实例化插件、注入服务、校验定义与使用配置；不启动后台工作。
func New(opts ...Option) (*App, error) {
	c := &config{}
	var errs []error
	for _, opt := range opts {
		if opt == nil {
			errs = append(errs, fmt.Errorf("%w: nil 选项", ErrInvalid))
			continue
		}
		if err := opt(c); err != nil {
			errs = append(errs, err)
		}
	}
	a, err := build(c)
	if err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return a, nil
}

// build 把选项变成装配结果。
// build 把 config 变成可运行的 App。分四段：校验配置 → 准备装配输入 → 装配 → 收尾校验。
func build(c *config) (*App, error) {
	if err := checkConfig(c); err != nil {
		return nil, err
	}
	in, err := prepareAssembly(c)
	if err != nil {
		return nil, err
	}
	asm, err := component.Assemble(in.spec)
	if err != nil {
		return nil, fmt.Errorf("tenon: 装配: %w", err)
	}
	return finishApp(c, in, asm)
}

// checkConfig 第一段：纯校验，不碰任何外部资源。把能提前发现的配置错误一次报全。
func checkConfig(c *config) error {
	var errs []error
	if c.profile != nil {
		if len(c.components) > 0 || len(c.without) > 0 {
			errs = append(errs, fmt.Errorf("%w: WithProfile 与 Default() / Minimal() / Without 二选一", ErrInvalid))
		}
	} else {
		if c.store == nil {
			errs = append(errs, fmt.Errorf("%w: 需要 WithStore（预设不含存储）", ErrInvalid))
		}
		if len(c.components) == 0 {
			errs = append(errs, fmt.Errorf("%w: 没有组件（先选 Default() 或 Minimal()）", ErrInvalid))
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// assemblyInput prepareAssembly 的产物：装配需要的全部输入与后面要复用的中间值。
type assemblyInput struct {
	workdir   string
	bus       *bus.Bus
	host      *host.Host
	ui        assemble.UI
	pui       *appapi.UI
	handle    assemble.DBHandle
	spec      component.Spec
	kern      assemble.Kernel
	appDefs   agentdef.Source
	builtin   *agentdef.MemSource
	toolNames []string
}

// prepareAssembly 第二段：整理组件清单、注入服务、构造 spec 与定义来源。
func prepareAssembly(c *config) (*assemblyInput, error) {
	names, errs, err := resolveComponents(c)
	if err != nil {
		return nil, err
	}
	kv := map[string]any{}
	if c.profile != nil {
		for k, v := range c.profile.Kernel {
			kv[k] = v
		}
	}
	for k, v := range c.kernel {
		kv[k] = v
	}
	kern, err := assemble.ParseKernel(kv)
	if err != nil {
		errs = append(errs, fmt.Errorf("%w: WithKernel: %v", ErrInvalid, err))
	}
	if c.sleepAfter > 0 {
		kern.SleepAfter = c.sleepAfter
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	workdir, err := resolveWorkdir(c.workdir)
	if err != nil {
		return nil, err
	}
	handle, hasSQL, err := resolveStore(c.store)
	if err != nil {
		return nil, err
	}
	b := bus.New(0)
	ui, pui, err := resolveUI(c, b)
	if err != nil {
		return nil, err
	}
	h := &host.Host{Bus: b, Workspace: c.workspace}
	inject := buildInject(c, h, ui, workdir, handle, hasSQL)
	appDefs, builtin, toolNames, extra, err := resolveDefinitionsAndTools(c)
	if err != nil {
		return nil, err
	}
	spec, err := buildSpec(c, names, extra, inject)
	if err != nil {
		return nil, err
	}
	return &assemblyInput{workdir: workdir, bus: b, host: h, ui: ui, pui: pui, handle: handle,
		spec: spec, kern: kern, appDefs: appDefs, builtin: builtin, toolNames: toolNames}, nil
}

// finishApp 第四段：装配完成后取服务、拼 App、校验定义。
func finishApp(c *config, in *assemblyInput, asm *component.Assembly) (*App, error) {
	defs, err := asm.Definitions(agentdef.Multi(in.appDefs, agentdef.Multi(pkgs.AgentSources(c.packages)...), in.builtin))
	if err != nil {
		return nil, fmt.Errorf("tenon: 定义: %w", err)
	}
	if len(c.packages) > 0 {
		asm.SetPackages(pkgs.Infos(context.Background(), c.packages, agentdef.Scope{Tenant: store.Tenant}))
	}
	st := in.handle
	if st == nil {
		if v, ok := asm.Value(assemble.ServiceDB); ok {
			st, _ = v.(assemble.DBHandle)
		}
		if st == nil {
			return nil, fmt.Errorf("%w: 没有存储：WithStore，或在 profile 里开一个提供 %s 的组件（如 store-sqlite）", ErrInvalid, assemble.ServiceDB)
		}
	}
	app := &App{asm: asm, defs: defs, host: in.host, bus: in.bus, handle: st, own: c.store, workdir: in.workdir,
		ui: in.ui, pui: in.pui, trusted: c.projectTrust, kern: in.kern, hooks: c.hooks, approvals: c.approvals,
		protocol: c.protocol, readyErrs: c.readyErrs, appDefs: in.appDefs, builtinDefs: in.builtin,
		appToolNames: in.toolNames, defaultAgent: c.defaultAgent,
		protocolUIOn: c.protocolUI || c.ui != nil, useSource: c.useSource}
	if v, ok := asm.Value(assemble.ServiceModelhub); ok {
		app.models, _ = v.(*assemble.Models)
	}
	if app.models == nil {
		return nil, fmt.Errorf("%w: 没有模型来源（WithModels 或 models 组件）", ErrInvalid)
	}
	if err := validateDefinitions(asm, defs); err != nil {
		return nil, err
	}
	return app, nil
}

// resolveComponents 整理组件清单：去重、应用 Without、WithModels 替换 models 组，
// 并处理「外部存储没有 SQL 出口时去掉 workgroup」。返回最终清单与已收集的错误。
func resolveComponents(c *config) ([]string, []error, error) {
	var errs []error
	preset := make([]string, 0, len(c.components))
	seen := map[string]bool{}
	for _, n := range c.components {
		if seen[n] {
			errs = append(errs, fmt.Errorf("%w: 组件 %s 重复", ErrInvalid, n))
			continue
		}
		seen[n] = true
		preset = append(preset, n)
	}
	for _, n := range c.without {
		if !seen[n] {
			errs = append(errs, fmt.Errorf("%w: Without 里的 %s 不在预设中", ErrInvalid, n))
			continue
		}
		delete(seen, n)
	}
	off := map[string]bool{}
	for _, n := range c.without {
		off[n] = true
	}
	names := make([]string, 0, len(preset))
	for _, n := range preset {
		if !off[n] {
			names = append(names, n)
		}
	}
	if c.models != nil && c.profile == nil {
		drop := map[string]bool{"models": true, "adapter-anthropic": true, "adapter-openai-chat": true, "adapter-openai-responses": true}
		kept := names[:0]
		for _, n := range names {
			if !drop[n] {
				kept = append(kept, n)
			}
		}
		names = kept
	}
	if c.profile == nil {
		if _, hasSQL := c.store.(store.SQL); !hasSQL {
			kept := names[:0]
			for _, n := range names {
				if n == "workgroup" {
					continue
				}
				kept = append(kept, n)
			}
			names = kept
		}
	}
	return names, errs, nil
}

// resolveWorkdir 工作目录：配置给了就用，否则取进程当前目录，统一转绝对路径。
func resolveWorkdir(dir string) (string, error) {
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("%w: 取工作目录: %v", ErrInvalid, err)
		}
		dir = wd
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("%w: 工作目录 %s: %v", ErrInvalid, dir, err)
	}
	return abs, nil
}

// resolveStore 存储句柄：WithStore 给的包一层；否则留 nil（由组件提供，装配后取 service:db）。
func resolveStore(s store.Store) (assemble.DBHandle, bool, error) {
	if s == nil {
		return nil, false, nil
	}
	_, hasSQL := s.(store.SQL)
	return &storeHandle{db: s, blobs: newMemArtifacts(s.Exists)}, hasSQL, nil
}

// resolveUI 界面出口：WithUI 与 WithProtocolUI 二选一；都没给用 nopUI。
func resolveUI(c *config, b *bus.Bus) (assemble.UI, *appapi.UI, error) {
	ui := c.ui
	switch {
	case c.protocolUI && ui != nil:
		return nil, nil, fmt.Errorf("%w: WithUI 与 WithProtocolUI 只能选一个", ErrInvalid)
	case c.protocolUI:
		pui := appapi.NewUI(b)
		return pui, pui, nil
	case ui == nil:
		return nopUI{}, nil, nil
	}
	return ui, nil, nil
}

// buildInject 装配期注入的服务表：内核服务 + 应用给的 + 包/存储/模型。
func buildInject(c *config, h *host.Host, ui assemble.UI, workdir string,
	handle assemble.DBHandle, hasSQL bool) map[string]any {
	inject := map[string]any{
		assemble.ServiceHost:    h,
		assemble.ServiceUI:      ui,
		assemble.ServiceWorkdir: workdir,
		assemble.ServiceTrust:   c.projectTrust,
	}
	for k, v := range c.services {
		inject[k] = v
	}
	// WithPackages 也把包预置给插件（等同 WithService(pkgs.Service, ps)）；显式 WithService 给过就不覆盖。
	if len(c.packages) > 0 {
		if _, ok := inject[pkgs.Service]; !ok {
			inject[pkgs.Service] = c.packages
		}
	}
	if handle != nil {
		inject[assemble.ServiceDB] = handle
	}
	if hasSQL {
		if sql, ok := c.store.(store.SQL); ok {
			inject[component.ServiceSQL] = sql
		}
	}
	if c.models != nil {
		inject[assemble.ServiceModelhub] = c.models
	}
	if c.layers != nil {
		inject[component.KeyLayers.Name()] = c.layers
	}
	return inject
}

// resolveDefinitionsAndTools 定义来源（应用 / 内置）与应用工具组件。
// 返回 appDefs、builtin、工具名、应用组件表。
func resolveDefinitionsAndTools(c *config) (agentdef.Source, *agentdef.MemSource, []string, map[string]component.Component, error) {
	extra := map[string]component.Component{}
	seenPreset := map[string]bool{}
	for _, n := range c.components {
		seenPreset[n] = true
	}
	for _, comp := range c.extra {
		n := comp.Name()
		if seenPreset[n] && !slices.Contains(c.without, n) {
			return nil, nil, nil, nil, fmt.Errorf("%w: 应用组件 %s 与预设组件重名", ErrInvalid, n)
		}
		if _, dup := extra[n]; dup {
			return nil, nil, nil, nil, fmt.Errorf("%w: 应用组件 %s 重复", ErrInvalid, n)
		}
		extra[n] = componentPlugin{c: comp}
	}
	inline := agentdef.Mem()
	for _, ag := range c.inline {
		if err := putAgent(inline, ag); err != nil {
			return nil, nil, nil, nil, err
		}
	}
	builtin := agentdef.Mem()
	appDefs := agentdef.Multi(append(append([]agentdef.Source(nil), c.agents...), inline)...)
	var toolNames []string
	if len(c.tools) > 0 {
		ts := make([]*toolhub.Tool, 0, len(c.tools))
		seen := map[string]bool{}
		for _, t := range c.tools {
			if seen[t.Name()] {
				return nil, nil, nil, nil, fmt.Errorf("%w: WithTools 里工具 %s 重复", ErrInvalid, t.Name())
			}
			seen[t.Name()] = true
			ts = append(ts, t.t)
			toolNames = append(toolNames, t.Name())
		}
		if _, dup := extra[appToolsName]; dup {
			return nil, nil, nil, nil, fmt.Errorf("%w: 组件名 %s 是 WithTools 用的，换个名字", ErrInvalid, appToolsName)
		}
		extra[appToolsName] = appTools{tools: ts}
	}
	return appDefs, builtin, toolNames, extra, nil
}

// buildSpec 组件装配输入：profile 模式按 profile 展开，预设模式按清单展开。
func buildSpec(c *config, names []string, extra map[string]component.Component, inject map[string]any) (component.Spec, error) {
	var spec component.Spec
	inSpec := map[string]bool{}
	if c.profile != nil {
		spec = c.profile.Spec(assemble.SlotDefaults())
		for i, e := range spec.Plugins {
			if ov := c.configs[e.Name]; len(ov) > 0 {
				cfg := map[string]any{}
				for k, v := range e.Config {
					cfg[k] = v
				}
				for k, v := range ov {
					cfg[k] = v
				}
				spec.Plugins[i].Config = cfg
			}
			inSpec[e.Name] = true
		}
		for _, n := range component.Registered() {
			inSpec[n] = true
		}
		spec.Inject, spec.Instances = inject, extra
	} else {
		spec = component.Spec{
			Plugins:   make([]component.Entry, 0, len(names)+len(extra)),
			Inject:    inject,
			Instances: extra,
			Defaults:  assemble.SlotDefaults(),
		}
		for _, n := range names {
			spec.Plugins = append(spec.Plugins, component.Entry{Name: n, Config: c.configs[n]})
			inSpec[n] = true
		}
	}
	for n := range extra {
		inSpec[n] = true
	}
	for n := range c.configs {
		if !inSpec[n] {
			return spec, fmt.Errorf("%w: WithConfig 的组件 %s 不在装配里", ErrInvalid, n)
		}
	}
	if c.secrets != nil {
		if spec.Defaults == nil {
			spec.Defaults = map[string]any{}
		}
		spec.Defaults[assemble.SlotSecrets] = assemble.Secrets(secretsFunc(c.secrets))
	}
	for n := range extra {
		spec.Plugins = append(spec.Plugins, component.Entry{Name: n, Config: c.configs[n]})
	}
	return spec, nil
}

// validateDefinitions 逐个可见定义校验 [use.*]（src=nil：只算定义层，租户/用户层在打开会话时叠加）。
func validateDefinitions(asm *component.Assembly, defs agentdef.Source) error {
	refs, err := agentdef.Visible(context.Background(), defs, agentdef.Scope{Tenant: store.Tenant})
	if err != nil {
		return fmt.Errorf("tenon: 列定义: %w", err)
	}
	schemas := asm.AgentSchemas()
	var errs []error
	for _, ref := range refs {
		def, err := defs.Load(context.Background(), ref)
		if err != nil {
			errs = append(errs, fmt.Errorf("tenon: 定义 %s: %w", ref, err))
			continue
		}
		if len(def.Use) == 0 {
			continue
		}
		if _, err := useconf.ResolveAll(context.Background(), schemas, def.Use, nil, store.Tenant, ""); err != nil {
			errs = append(errs, fmt.Errorf("tenon: 定义 %s: %w", ref, err))
		}
	}
	return errors.Join(errs...)
}

// App 一个装配好的应用；Start 之后才能跑会话。
type App struct {
	mu        sync.Mutex
	asm       *component.Assembly
	defs      agentdef.Source
	host      *host.Host
	bus       *bus.Bus
	handle    assemble.DBHandle
	own       store.Store // WithStore 给的存储（Close 时关；组件提供的由组件作用域关）
	hooks     func(*assemble.SessionCx) agent.Hooks
	approvals adapters.ModeApprovals
	protocol  []func(*appapi.Options)
	readyErrs func([]error)
	// 外观用的（agents.go、session.go）。
	useSource    useconf.Source
	appDefs      agentdef.Source
	builtinDefs  *agentdef.MemSource
	appToolNames []string
	defaultAgent string
	protocolUIOn bool // 有人能回答确认（WithProtocolUI 或 WithUI）
	opened       bool // 组件已启动（Open）
	models       *assemble.Models
	workdir      string
	ui           assemble.UI
	pui          *appapi.UI // WithProtocolUI 时的协议界面
	trusted      bool
	api          *appapi.App // 应用协议层：Start 里建
	scope        *component.Scope
	builder      *assemble.Builder
	// kern 内核与宿主参数（WithKernel、WithSleepAfter）；bgStop 后台循环的取消函数。
	kern    assemble.Kernel
	bgStop  context.CancelFunc
	started bool
	closed  bool
}

// —— 存储句柄：外部 store.Store + 内存工件 —— //

// storeHandle 把外部存储包成 runtime.DBHandle。
type storeHandle struct {
	db    store.Store
	blobs toolhub.Artifacts
}

func (h *storeHandle) DB() store.Store { return h.db }

func (h *storeHandle) Blobs() toolhub.Artifacts { return h.blobs }
