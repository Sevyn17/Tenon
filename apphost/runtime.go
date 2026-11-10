package apphost

// 进程装配与应用接口层构造（原 apps/tenon/main.go 的 withRuntime / chat 前半段），CLI 与桌面端共用。
// 顺序：读信任 → 读 profile → 建总线与宿主 → 装配（预置宿主 / 界面 / 工作目录 / 信任）→ 启动进程作用域
// → 收集点位、建会话构造器 → appapi.New → 跑 ready 点位。

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/Sevyn17/tenon"
	"github.com/Sevyn17/tenon/appapi"
	compact "github.com/Sevyn17/tenon/modules/context/compact-basic"
	"github.com/Sevyn17/tenon/modules/security/permission"
	"github.com/Sevyn17/tenon/runtime/agent"
	"github.com/Sevyn17/tenon/runtime/agentdef"
	"github.com/Sevyn17/tenon/runtime/assemble"
	"github.com/Sevyn17/tenon/runtime/bus"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/host"
	"github.com/Sevyn17/tenon/runtime/pkgs"
	"github.com/Sevyn17/tenon/runtime/store"
)

// Flags 命令行参数（CLI 与桌面端同名同义）。
type Flags struct {
	Base, Model, Workdir, Profile, Sandbox, MCP, Mode string
	Yes, ProjectHooks, TrustProject                   bool
	MaxSteps                                          int // -1 = 未指定
	// BaseProfile 产品内置的 base profile 原文（最底层，其余各层叠在上面）；空 = DefaultProfileTOML。
	BaseProfile string
}

// Overrides 命令行参数 → 插件配置覆盖（只覆盖给了值的项）。
func Overrides(f Flags, root string) map[string]map[string]any {
	// 会话库位置由 store-sqlite 自己决定（缺省用户级目录），这里不覆盖：覆盖在 profile 之后合并，会盖掉用户的配置。
	ov := map[string]map[string]any{}
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

// Runtime 打开了的工作区：建在 SDK 的 tenon.App 上（计划/07 阶段三），这里加上 profile 分层、项目信任、
// 插件包与命令行覆盖。组件已启动（存储已打开）；跑会话要再调 BuildApp。用完调 Close。
type Runtime struct {
	App     *tenon.App
	Rt      *Assembled
	Profile *component.Profile
	Root    string
	Bus     *bus.Bus
	Host    *host.Host
	UI      assemble.UI // StartOptions.UI；nil = 协议界面（提问经协议回答）
	// 项目信任结果与装配提示（横幅 / explain 显示）。
	Trusted  bool
	TrustWhy string
	Notes    []string
	// Packages 已加载的插件包（用户级在前、项目级在后）。
	Packages []*pkgs.Package
	// ExtraProfile -profile 指定的文件（plugins.set 重新合并时用）。
	ExtraProfile string
	// BaseProfile 产品内置的 base profile 原文（同 Flags.BaseProfile）。
	BaseProfile string

	readyErrs []error // ready 点位的错误（BuildApp 时跑，Built.Ready 交出）
}

// StartOptions Start 的参数。
type StartOptions struct {
	Flags Flags
	// AskTrust 无信任记录且项目带 .tenon 配置时询问用户；nil = 按不受信。
	AskTrust func(key string) bool
	// UI 插件的界面（service:ui），如终端界面；nil = 协议界面：输出发成 notice、提问登记成交互，由前端回答。
	UI assemble.UI
	// SleepAfter 空闲会话的休眠超时（0 = 不休眠，02 2.2）。>0 时同时启动兜底扫描。
	SleepAfter time.Duration
	// App 跑会话时的选项（BuildApp 用；会话钩子等要在装配时给，所以放在这里）。
	App AppOptions
}

// AppOptions 跑会话时的选项。
type AppOptions struct {
	MaxSteps int // -1 = 用 profile [kernel] 的缺省
	// Hooks 应用自己的会话钩子（CLI 打印正文）；可空。appapi 的流式事件钩子总会并入。
	Hooks func(*assemble.SessionCx) agent.Hooks
	// ModesWorkdir 项目档位确认记录（trust.toml）用的目录；空 = 工作目录。
	ModesWorkdir string
	// Upload 传输层会挂 appapi.UploadHandler（网页服务、桌面端）：hello 报 upload 能力。
	Upload bool
	// Workspaces 工作区管理器（workspace.list / workspace.open）；nil = 不能切换。
	Workspaces appapi.Workspaces
}

// Start 读信任与 profile，用 SDK 按 profile 装配（tenon.WithProfile）并启动组件（打开存储）。
// 失败时已建的资源全部回收。
func Start(o StartOptions) (*Runtime, error) {
	root, err := filepath.Abs(o.Flags.Workdir)
	if err != nil {
		return nil, err
	}
	trusted, why, err := ResolveTrust(o.Flags.TrustProject || o.Flags.ProjectHooks, root, o.AskTrust)
	if err != nil {
		return nil, err
	}
	p, ps, notes, err := LoadAll(o.Flags.BaseProfile, o.Flags.Profile, root, trusted)
	if err != nil {
		return nil, fmt.Errorf("读 profile: %w", err)
	}
	r := &Runtime{Profile: p, Root: root, UI: o.UI, Trusted: trusted, TrustWhy: why, Notes: notes, Packages: ps,
		ExtraProfile: o.Flags.Profile, BaseProfile: o.Flags.BaseProfile}
	modesWD := o.App.ModesWorkdir
	if modesWD == "" {
		modesWD = root
	}
	opts := []tenon.Option{
		tenon.WithProfile(p), tenon.WithWorkdir(root), tenon.WithProjectTrust(trusted),
		tenon.WithWorkspace(WorkspaceKey(root)), tenon.WithService(pkgs.Service, ps),
		tenon.WithAgents(pkgs.AgentSources(ps)...), // 包带的定义：用户级在前，同名时优先于项目级（T4d）
		tenon.WithSleepAfter(o.SleepAfter), tenon.WithModeApprovals(TrustApprovals{Root: modesWD}),
		tenon.WithReadyErrors(func(errs []error) { r.readyErrs = errs }),
		tenon.WithProtocolOptions(func(ao *appapi.Options) {
			ao.Profile, ao.Packages = p, ps
			ao.Profiles = Profiles{Base: o.Flags.BaseProfile, Extra: o.Flags.Profile, Root: root, Trusted: trusted}
			ao.Upload, ao.Workspaces = o.App.Upload, o.App.Workspaces
		}),
	}
	for name, kv := range Overrides(o.Flags, root) { // 命令行参数：叠在 profile 的组件配置上
		opts = append(opts, tenon.WithConfig(name, kv))
	}
	if o.App.MaxSteps >= 0 {
		opts = append(opts, tenon.WithKernel(map[string]any{"max_steps": o.App.MaxSteps}))
	}
	if o.App.Hooks != nil {
		opts = append(opts, tenon.WithSessionHooks(o.App.Hooks))
	}
	if o.UI != nil {
		opts = append(opts, tenon.WithUI(o.UI))
	} else {
		opts = append(opts, tenon.WithProtocolUI())
	}
	app, err := tenon.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("装配: %w", err)
	}
	if err := app.Open(context.Background()); err != nil {
		_ = app.Close(context.Background())
		return nil, err
	}
	in := app.Internals()
	in.Assembly.SetPackages(pkgs.Infos(context.Background(), ps, agentdef.Scope{Tenant: store.Tenant}))
	r.App, r.Bus, r.Host = app, in.Bus, in.Host
	r.Rt = &Assembled{Asm: in.Assembly, Models: in.Models, Defs: in.Definitions}
	if v, ok := in.Assembly.Value(assemble.ServiceDB); ok {
		r.Rt.Store, _ = v.(assemble.DBHandle)
	}
	if v, ok := in.Assembly.Value(assemble.ServiceCompactor); ok {
		r.Rt.Compact, _ = v.(*compact.Compactor)
	}
	if r.Rt.Store == nil { // WithStore 不在这条路上：存储一定来自组件
		r.Close()
		return nil, fmt.Errorf("装配: 没有提供 %s 的存储组件", assemble.ServiceDB)
	}
	r.Notes = append(r.Notes, r.importLegacy(context.Background())...) // 旧版项目里的会话库并入用户级库
	return r, nil
}

// WorkspaceKey 会话库里工作区的键：规范化后的绝对路径（Windows 上不分大小写）。会话库是用户级的，
// 同一个目录换种写法打开也要落到同一组会话上。
func WorkspaceKey(root string) string { return NormPath(root) }

// Close 关闭组件、存储与总线。可重复调用。
func (r *Runtime) Close() {
	if r.App != nil {
		_ = r.App.Close(context.Background())
	}
}

// Built BuildApp 的结果。
type Built struct {
	API     *appapi.App
	DB      store.Store
	Model   string
	Perms   *permission.Sessions // 未装 permission 插件时为 nil
	Builder *assemble.Builder
	ready   []error
}

// Ready ready 点位（如工作组补投会打开领导会话）的错误。点位在 BuildApp 里已经跑过；这里交出错误，
// 调用方当提示显示（不让启动失败）。
func (b *Built) Ready(context.Context) []error { return b.ready }

// BuildApp 接上会话构造器与 [kernel] 宿主参数、应用协议层，跑 ready 点位，再启动后台循环（tenon.App.Start）。
// 只跑一次性子命令（export / explain）的不调它，也就不起后台循环。
func (r *Runtime) BuildApp() (*Built, error) {
	if r.App.Internals().Models == nil {
		return nil, fmt.Errorf("profile 中没有 models 插件")
	}
	if err := r.App.Start(context.Background()); err != nil {
		return nil, err
	}
	in := r.App.Internals()
	b := &Built{API: r.App.API(), DB: in.DB, Model: in.Models.Model, Builder: in.Builder, ready: r.readyErrs}
	if v, ok := in.Assembly.Value(permission.Service); ok {
		b.Perms, _ = v.(*permission.Sessions)
	}
	return b, nil
}
