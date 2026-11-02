package boot

// 进程装配与应用接口层构造（原 apps/ybase/main.go 的 withRuntime / chat 前半段），CLI 与桌面端共用。
// 顺序：读信任 → 读 profile → 建总线与宿主 → 装配（预置宿主 / 界面 / 工作目录 / 信任）→ 启动进程作用域
// → 收集点位、建会话构造器 → appapi.New → 跑 ready 点位。

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"ybase-kernel/core/agent"
	"ybase-kernel/core/appapi"
	"ybase-kernel/core/bus"
	"ybase-kernel/core/host"
	"ybase-kernel/core/pkgs"
	"ybase-kernel/core/plugin"
	"ybase-kernel/core/runtime"
	"ybase-kernel/core/session"
	"ybase-kernel/core/store"
	"ybase-kernel/plugins/security/permission"
)

// Flags 命令行参数（CLI 与桌面端同名同义）。
type Flags struct {
	Base, Model, Workdir, Profile, Sandbox, MCP, Mode string
	Yes, ProjectHooks, TrustProject                   bool
	MaxSteps                                          int // -1 = 未指定
}

// Overrides 命令行参数 → 插件配置覆盖（只覆盖给了值的项）。
func Overrides(f Flags, root string) map[string]map[string]any {
	ov := map[string]map[string]any{
		"store-sqlite": {"dir": filepath.Join(root, ".ybase")},
	}
	set := func(plugin, key string, v any) {
		if ov[plugin] == nil {
			ov[plugin] = map[string]any{}
		}
		ov[plugin][key] = v
	}
	if f.Base != "" {
		set("models", "base_url", f.Base)
	}
	if f.Model != "" {
		set("models", "model", f.Model)
	}
	switch {
	case f.Mode != "":
		set("permission", "force_mode", f.Mode)
	case f.Yes:
		set("permission", "force_mode", string(permission.Full))
	}
	if f.Sandbox != "" {
		set("permission", "sandbox", f.Sandbox)
	}
	if f.ProjectHooks {
		set("hooks", "project_hooks", true)
	}
	if f.MCP != "" {
		set("mcp", "config", f.MCP)
	}
	return ov
}

// Runtime 装配好且已启动的进程：插件运行时 + 应用预置给插件的服务。用完调 Close。
type Runtime struct {
	Rt      *Assembled
	Profile *plugin.Profile
	Root    string
	Bus     *bus.Bus
	Host    *host.Host
	UI      runtime.UI
	// 项目信任结果与装配提示（横幅 / explain 显示）。
	Trusted  bool
	TrustWhy string
	Notes    []string
	// Packages 已加载的插件包（用户级在前、项目级在后）。
	Packages []*pkgs.Package

	proc      *plugin.Scope
	sleepStop context.CancelFunc // 兜底扫描循环的取消函数（SleepAfter>0 时）
}

// StartOptions Start 的参数。
type StartOptions struct {
	Flags Flags
	// AskTrust 无信任记录且项目带 .ybase 配置时询问用户；nil = 按不受信。
	AskTrust func(key string) bool
	// UI 用总线构造界面服务（service:ui）；必填。
	UI func(b *bus.Bus) runtime.UI
	// SleepAfter 空闲会话的休眠超时（0 = 不休眠，02 2.2）。>0 时同时启动兜底扫描。
	SleepAfter time.Duration
}

// Start 读信任与 profile、装配并启动进程作用域。失败时已建的资源全部回收。
func Start(o StartOptions) (*Runtime, error) {
	root, err := filepath.Abs(o.Flags.Workdir)
	if err != nil {
		return nil, err
	}
	trusted, why, err := ResolveTrust(o.Flags.TrustProject || o.Flags.ProjectHooks, root, o.AskTrust)
	if err != nil {
		return nil, err
	}
	p, ps, notes, err := LoadAll(o.Flags.Profile, root, trusted)
	if err != nil {
		return nil, fmt.Errorf("读 profile: %w", err)
	}
	b := bus.New(0)
	r := &Runtime{Profile: p, Root: root, Bus: b, Host: &host.Host{Bus: b}, UI: o.UI(b),
		Trusted: trusted, TrustWhy: why, Notes: notes, Packages: ps}
	r.Rt, err = Assemble(p, Overrides(o.Flags, root), map[string]any{
		runtime.ServiceWorkdir: root,
		runtime.ServiceHost:    r.Host,
		runtime.ServiceUI:      r.UI,
		runtime.ServiceTrust:   trusted,
		pkgs.Service:           ps,
	})
	if err != nil {
		b.Close()
		return nil, fmt.Errorf("装配: %w", err)
	}
	r.proc = plugin.NewRoot("process")
	if err := r.Rt.Asm.Start(r.proc); err != nil {
		r.Close()
		return nil, err
	}
	r.Host.DB = r.Rt.Store.DB() // 数据库在 store-sqlite 启动后才打开
	r.Host.SleepAfter = o.SleepAfter
	if o.SleepAfter > 0 { // 休眠会话的兜底扫描（02 2.2）：有信件的休眠会话定期唤醒
		ctx, cancel := context.WithCancel(context.Background())
		r.sleepStop = cancel
		go r.Host.ScanLoop(ctx, o.SleepAfter)
	}
	return r, nil
}

// Close 关闭进程作用域与总线（先插件后总线，与原 withRuntime 的 defer 顺序一致）。
func (r *Runtime) Close() {
	if r.sleepStop != nil {
		r.sleepStop()
		r.sleepStop = nil
	}
	if r.proc != nil {
		r.proc.Close(context.Background())
		r.proc = nil
	}
	if r.Bus != nil {
		r.Bus.Close()
	}
}

// AppOptions BuildApp 的参数。
type AppOptions struct {
	MaxSteps int // -1 = 用 profile [kernel] 的缺省
	// Hooks 应用自己的会话钩子（CLI 打印正文）；可空。appapi 的流式事件钩子总会并入。
	Hooks func(*runtime.SessionCx) agent.Hooks
	// UI 事件化界面（桌面端 / api-server）；CLI 用终端界面，传 nil。
	UI *appapi.UI
	// ModesWorkdir 项目档位确认记录（trust.toml）用的目录；CLI 沿用 -dir 原值。
	ModesWorkdir string
}

// Built BuildApp 的结果。
type Built struct {
	API     *appapi.App
	DB      store.Store
	Model   string
	Perms   *permission.Sessions // 未装 permission 插件时为 nil
	Builder *runtime.Builder
	ready   []runtime.ReadyFunc
}

// Ready 跑 ready 点位（构造器就绪后再跑：如工作组补投会打开领导会话），返回各自的错误。
func (b *Built) Ready(ctx context.Context) []error {
	var errs []error
	for _, f := range b.ready {
		if err := f(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// BuildApp 收集点位、建会话构造器并构造应用接口层（不跑 ready，调用方在接好输出后调 Built.Ready）。
func (r *Runtime) BuildApp(o AppOptions) (*Built, error) {
	rt := r.Rt
	if rt.Models == nil {
		return nil, fmt.Errorf("profile 中没有 models 插件")
	}
	db := rt.Store.DB()
	modelName := rt.Models.Model
	var perms *permission.Sessions
	if v, ok := rt.Asm.Value(permission.Service); ok {
		perms, _ = v.(*permission.Sessions)
	}
	kern, err := runtime.ParseKernel(r.Profile.Kernel)
	if err != nil {
		return nil, fmt.Errorf("profile [kernel]: %w", err)
	}
	col, err := runtime.Collect(rt.Asm)
	if err != nil {
		return nil, err
	}
	// 每回合开始的档位变化提示由 appapi 发 notice 事件。
	// 组件登记的守卫规则（04 4.2、08 D4）：投递/建会话等内核点位在 Host 里执行。
	r.Host.Guards = rt.Asm.GuardRules()
	r.Host.Hooks.AfterTurn = func(id string, end session.EndReason) {
		for _, f := range col.AfterTurn {
			f(id, end)
		}
	}
	maxSteps := o.MaxSteps
	rb := &runtime.Builder{DB: db, Artifacts: rt.Store.Blobs(), Models: rt.Models.Hub, Model: modelName, Workdir: r.Root,
		Policy: func() session.Policy {
			pol := kern.Policy
			if maxSteps >= 0 {
				pol.MaxSteps = uint32(maxSteps)
			}
			return pol
		},
		Tools:  col.Tools,
		Layers: col.Layers,
		Items:  col.Items,
		Kernel: &kern,
		// 会话定义来源：子智能体 / 工作组成员按内置模板名构造（T1b-2c）。
		Definitions: rt.Defs,
		// 状态提交登记（框架设计 12.5）：agent 每次调模型前收集变化的片段。
		Statuses: col.Statuses,
		// 使用配置原型（06 6.2）：定义里的 [use.*] 按它算成会话的 use_config。
		Schemas: rt.Asm.AgentSchemas(),
	}
	rb.Hooks = o.Hooks
	rb.Blocks = col.Blocks // 系统提示词由 prompt 插件提供（plugin-switch-plan §2.1）
	// 应用接口层：设置会话构造器（顶层会话带委派 / 领导工具；子会话与工作组成员按 child 构造，深度 1）、
	// 把提交事件接到总线、档位变化提示发 notice。
	ao := appapi.Options{Host: r.Host, Bus: r.Bus, DB: db, Builder: rb, Model: modelName, Workdir: r.Root,
		Trusted: r.Trusted, Profile: r.Profile, Packages: r.Packages, UI: o.UI}
	if perms != nil {
		ao.Modes = PermModes{P: perms, Workdir: o.ModesWorkdir}
		// 项目档位当新会话缺省时，也要用户确认过放宽（与 /mode 切换共用 trust.toml 的确认记录）。
		wd := o.ModesWorkdir
		perms.ModeApproved = func(m permission.Mode, widened []string) bool {
			return ModeApproved(wd, string(m), strings.Join(widened, "、"))
		}
	}
	if rt.Compact != nil {
		ao.Compactor = rt.Compact
	}
	api, err := appapi.New(ao)
	if err != nil {
		return nil, err
	}
	return &Built{API: api, DB: db, Model: modelName, Perms: perms, Builder: rb, ready: col.Ready}, nil
}
