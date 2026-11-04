// Package ybase 底座对外的 SDK 入口（框架设计 07 - SDK 接口，T1b-2d）。
//
// 一个应用 = 组件清单 + 存储 + 模型 + 定义来源：
//
//	app, err := ybase.New(ybase.Default(),
//	    ybase.WithStore(db), ybase.WithAgents(ybase.Dir("./agents")))
//	if err != nil { ... }
//	defer app.Close(context.Background())
//	if err := app.Start(ctx); err != nil { ... }
//	reply, err := app.Ask(ctx, "support", "你好", nil)
//
// New 只装配与校验，不启动后台工作；未 Start 调用运行接口返回 ErrNotStarted。
// 校验错误（未知组件、重复组件、定义 [use.*] 写错……）在 New 里一次报完。
//
// 组件配置走代码（06 - 配置）：本包不读 ybase.toml，也不做环境变量覆盖。
package ybase

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"ybase/runtime/agentdef"
	"ybase/runtime/assemble"
	"ybase/runtime/bus"
	"ybase/runtime/component"
	"ybase/runtime/host"
	"ybase/runtime/modelhub"
	"ybase/runtime/store"
	"ybase/runtime/toolhub"
	"ybase/runtime/useconf"
)

// 错误（07 7.3；errors.Is 可判）。
var (
	ErrNotStarted = errors.New("ybase: 未 Start")
	ErrInvalid    = errors.New("ybase: 参数不合法")
	ErrNotFound   = errors.New("ybase: 未找到")
)

// Component 应用自带的组件（04 4.2：Name + Setup），需要后台工作的实现 Start。
// 内置组件由预设提供，应用一般只在 With 里加自己的组件。
type Component interface {
	Name() string
	Setup(cx *component.Registrar) error
	Start(sc *component.Scope) error
}

// componentPlugin 把 Component 适配成 component.Component（装配输入用）。
type componentPlugin struct{ c Component }

func (p componentPlugin) Meta() component.Meta {
	return component.Meta{Name: p.c.Name(), Default: true}
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
	workdir    string
	ui         assemble.UI
	// sleepAfter 空闲会话休眠超时（0 = 不休眠，02 2.2）；>0 时启动兜底扫描。
	sleepAfter time.Duration
	// projectTrust 工作目录是否受信（WithProjectTrust）；缺省 false。
	projectTrust bool
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
func WithStore(s store.Store) Option {
	return func(c *config) error {
		if s == nil {
			return fmt.Errorf("%w: WithStore 传了 nil", ErrInvalid)
		}
		c.store = s
		return nil
	}
}

// WithModels 用现成的模型来源替换预设里的 models + 适配器（测试、嵌入方自带模型时用）。
func WithModels(hub *modelhub.Hub, model string) Option {
	return func(c *config) error {
		if hub == nil {
			return fmt.Errorf("%w: WithModels 的 hub 为 nil", ErrInvalid)
		}
		c.models = &assemble.Models{Hub: hub, Model: model}
		return nil
	}
}

// WithAgents 追加定义来源（可多个；平台层由组件模板补齐）。
func WithAgents(srcs ...agentdef.Source) Option {
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
// root/user/<租户>/<用户>/<名>。
func Dir(root string) agentdef.Source { return agentdef.Dir(root) }

// WithWorkdir 会话工作目录（fs / shell / instructions 等组件要用）。
func WithWorkdir(dir string) Option {
	return func(c *config) error {
		c.workdir = dir
		return nil
	}
}

// WithUI 界面输出（缺省丢弃输出）。
func WithUI(ui assemble.UI) Option {
	return func(c *config) error {
		if ui == nil {
			return fmt.Errorf("%w: WithUI 传了 nil", ErrInvalid)
		}
		c.ui = ui
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

// WithProjectTrust 声明工作目录受信：受信时才读其中的 .ybase/hooks.toml、mcp.toml、项目权限档位、
// AGENTS.md 与项目技能。缺省不受信——嵌入方指向任意目录时不执行目录里的钩子与 MCP 命令
// （2026-11-04 审查 P1-5：原先写死受信）。
func WithProjectTrust(trusted bool) Option {
	return func(c *config) error {
		c.projectTrust = trusted
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
func build(c *config) (*App, error) {
	var errs []error
	if c.store == nil {
		errs = append(errs, fmt.Errorf("%w: 需要 WithStore（预设不含存储）", ErrInvalid))
	}
	if len(c.components) == 0 {
		errs = append(errs, fmt.Errorf("%w: 没有组件（先选 Default() 或 Minimal()）", ErrInvalid))
	}
	// 预设组件名 → 去重、应用 Without、WithModels 替换 models 组。
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
	if c.models != nil {
		drop := map[string]bool{"models": true, "adapter-anthropic": true, "adapter-openai-chat": true, "adapter-openai-responses": true}
		kept := names[:0]
		for _, n := range names {
			if !drop[n] {
				kept = append(kept, n)
			}
		}
		names = kept
	}
	// 应用组件：名字不能与预设重复。
	extra := map[string]component.Component{}
	for _, comp := range c.extra {
		n := comp.Name()
		if seen[n] && !off[n] {
			errs = append(errs, fmt.Errorf("%w: 应用组件 %s 与预设组件重名", ErrInvalid, n))
			continue
		}
		if _, dup := extra[n]; dup {
			errs = append(errs, fmt.Errorf("%w: 应用组件 %s 重复", ErrInvalid, n))
			continue
		}
		extra[n] = componentPlugin{c: comp}
	}
	// 外部存储没有 SQL 出口时，要求 SQL 的组件起不来：直接去掉并记录说明（不悄悄报错）。
	sql, hasSQL := c.store.(store.SQL)
	if !hasSQL || c.store == nil {
		kept := names[:0]
		for _, n := range names {
			if n == "workgroup" {
				continue
			}
			kept = append(kept, n)
		}
		names = kept
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	workdir := c.workdir
	if workdir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("%w: 取工作目录: %v", ErrInvalid, err)
		}
		workdir = wd
	}
	abs, err := filepath.Abs(workdir)
	if err != nil {
		return nil, fmt.Errorf("%w: 工作目录 %s: %v", ErrInvalid, workdir, err)
	}
	workdir = abs
	ui := c.ui
	if ui == nil {
		ui = nopUI{}
	}
	st := &storeHandle{db: c.store, blobs: &memArtifacts{m: map[string][]byte{}}}
	b := bus.New(0)
	h := &host.Host{Bus: b}
	inject := map[string]any{
		assemble.ServiceHost:    h,
		assemble.ServiceUI:      ui,
		assemble.ServiceWorkdir: workdir,
		assemble.ServiceTrust:   c.projectTrust,
		assemble.ServiceDB:      assemble.DBHandle(st),
	}
	if hasSQL {
		inject[component.ServiceSQL] = sql
	}
	if c.models != nil {
		inject[assemble.ServiceModelhub] = c.models
	}
	// 定义来源：组件模板（装配期登记）+ 应用来源；平台同名冲突在这里报。
	appDefs := agentdef.Multi(c.agents...)
	spec := component.Spec{
		Plugins:   make([]component.Entry, 0, len(names)+len(extra)),
		Inject:    inject,
		Instances: extra,
		Defaults:  assemble.SlotDefaults(),
	}
	for _, n := range names {
		spec.Plugins = append(spec.Plugins, component.Entry{Name: n})
	}
	for n := range extra {
		spec.Plugins = append(spec.Plugins, component.Entry{Name: n})
	}
	asm, err := component.Assemble(spec)
	if err != nil {
		return nil, fmt.Errorf("ybase: 装配: %w", err)
	}
	defs, err := asm.Definitions(appDefs)
	if err != nil {
		return nil, fmt.Errorf("ybase: 定义: %w", err)
	}
	app := &App{asm: asm, defs: defs, host: h, bus: b, handle: st, workdir: workdir, ui: ui, sleepAfter: c.sleepAfter}
	if v, ok := asm.Value(assemble.ServiceModelhub); ok {
		app.models, _ = v.(*assemble.Models)
	}
	if app.models == nil {
		return nil, fmt.Errorf("%w: 没有模型来源（WithModels 或 models 组件）", ErrInvalid)
	}
	// 定义里的 [use.*] 在 New 期一次校验（09 9.3、03 3.6）：未知组件 / 未知字段都这时报。
	if err := validateDefinitions(asm, defs); err != nil {
		return nil, err
	}
	return app, nil
}

// validateDefinitions 逐个可见定义校验 [use.*]（src=nil：只算定义层，租户/用户层在打开会话时叠加）。
func validateDefinitions(asm *component.Assembly, defs agentdef.Source) error {
	refs, err := agentdef.Visible(context.Background(), defs, agentdef.Scope{Tenant: store.Tenant})
	if err != nil {
		return fmt.Errorf("ybase: 列定义: %w", err)
	}
	schemas := asm.AgentSchemas()
	var errs []error
	for _, ref := range refs {
		def, err := defs.Load(context.Background(), ref)
		if err != nil {
			errs = append(errs, fmt.Errorf("ybase: 定义 %s: %w", ref, err))
			continue
		}
		if len(def.Use) == 0 {
			continue
		}
		if _, err := useconf.ResolveAll(context.Background(), schemas, def.Use, nil, store.Tenant, ""); err != nil {
			errs = append(errs, fmt.Errorf("ybase: 定义 %s: %w", ref, err))
		}
	}
	return errors.Join(errs...)
}

// App 一个装配好的应用；Start 之后才能跑会话。
type App struct {
	mu      sync.Mutex
	asm     *component.Assembly
	defs    agentdef.Source
	host    *host.Host
	bus     *bus.Bus
	handle  *storeHandle
	models  *assemble.Models
	workdir string
	ui      assemble.UI
	scope   *component.Scope
	builder *assemble.Builder
	// sleepAfter / sleepStop：空闲休眠超时与兜底扫描的取消函数（02 2.2）。
	sleepAfter time.Duration
	sleepStop  context.CancelFunc
	started    bool
	closed     bool
}

// —— 存储句柄：外部 store.Store + 内存工件 —— //

// storeHandle 把外部存储包成 runtime.DBHandle。
type storeHandle struct {
	db    store.Store
	blobs toolhub.Artifacts
}

func (h *storeHandle) DB() store.Store { return h.db }

func (h *storeHandle) Blobs() toolhub.Artifacts { return h.blobs }
