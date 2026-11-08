package apphost

// 进程装配与应用接口层构造（原 apps/tenon/main.go 的 withRuntime / chat 前半段），CLI 与桌面端共用。
// 顺序：读信任 → 读 profile → 建总线与宿主 → 装配（预置宿主 / 界面 / 工作目录 / 信任）→ 启动进程作用域
// → 收集点位、建会话构造器 → appapi.New → 跑 ready 点位。

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"tenon/appapi"
	"tenon/appapi/wire"
	"tenon/modules/security/permission"
	"tenon/modules/tools/fs"
	"tenon/runtime/agent"
	"tenon/runtime/assemble"
	"tenon/runtime/bus"
	"tenon/runtime/component"
	"tenon/runtime/host"
	"tenon/runtime/pkgs"
	"tenon/runtime/store"
)

// Flags 命令行参数（CLI 与桌面端同名同义）。
type Flags struct {
	// BaseProfile 产品内置的 base profile 原文（最底层，其余各层叠在上面）；空 = DefaultProfileTOML。
	BaseProfile                                       string
	Base, Model, Workdir, Profile, Sandbox, MCP, Mode string
	Yes, ProjectHooks, TrustProject                   bool
	MaxSteps                                          int // -1 = 未指定
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

// Runtime 装配好且已启动的进程：插件运行时 + 应用预置给插件的服务。用完调 Close。
type Runtime struct {
	Rt      *Assembled
	Profile *component.Profile
	Root    string
	Bus     *bus.Bus
	Host    *host.Host
	UI      assemble.UI
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

	proc       *component.Scope
	sleepAfter time.Duration
	bgStop     context.CancelFunc // 后台循环（兜底扫描、存储清理、发件箱）的取消函数；BuildApp 之后才有
}

// StartOptions Start 的参数。
type StartOptions struct {
	Flags Flags
	// AskTrust 无信任记录且项目带 .tenon 配置时询问用户；nil = 按不受信。
	AskTrust func(key string) bool
	// UI 用总线构造界面服务（service:ui）；必填。
	UI func(b *bus.Bus) assemble.UI
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
	p, ps, notes, err := LoadAll(o.Flags.BaseProfile, o.Flags.Profile, root, trusted)
	if err != nil {
		return nil, fmt.Errorf("读 profile: %w", err)
	}
	b := bus.New(0)
	r := &Runtime{Profile: p, Root: root, Bus: b, Host: &host.Host{Bus: b, Workspace: WorkspaceKey(root)}, UI: o.UI(b),
		Trusted: trusted, TrustWhy: why, Notes: notes, Packages: ps, ExtraProfile: o.Flags.Profile, BaseProfile: o.Flags.BaseProfile}
	r.Rt, err = Assemble(p, Overrides(o.Flags, root), map[string]any{
		assemble.ServiceWorkdir: root,
		assemble.ServiceHost:    r.Host,
		assemble.ServiceUI:      r.UI,
		assemble.ServiceTrust:   trusted,
		pkgs.Service:            ps,
	})
	if err != nil {
		b.Close()
		return nil, fmt.Errorf("装配: %w", err)
	}
	r.proc = component.NewRoot("process")
	if err := r.Rt.Asm.Start(r.proc); err != nil {
		r.Close()
		return nil, err
	}
	r.Host.DB = r.Rt.Store.DB()                                        // 数据库在 store-sqlite 启动后才打开
	r.Notes = append(r.Notes, r.importLegacy(context.Background())...) // 旧版项目里的会话库并入用户级库
	// 宿主参数来自 profile 的 [kernel] 节：这里先校验（写错启动即报），BuildApp 里再按 Profile 解析接上。
	if _, err = assemble.ParseKernel(p.Kernel); err != nil {
		r.Close()
		return nil, fmt.Errorf("profile [kernel]: %w", err)
	}
	r.sleepAfter = o.SleepAfter
	return r, nil
}

// WorkspaceKey 会话库里工作区的键：规范化后的绝对路径（Windows 上不分大小写）。会话库是用户级的，
// 同一个目录换种写法打开也要落到同一组会话上。
func WorkspaceKey(root string) string { return NormPath(root) }

// Close 关闭进程作用域与总线（先插件后总线，与原 withRuntime 的 defer 顺序一致）。
func (r *Runtime) Close() {
	if r.bgStop != nil {
		r.bgStop()
		r.bgStop = nil
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
	Hooks func(*assemble.SessionCx) agent.Hooks
	// UI 事件化界面（桌面端 / api-server）；CLI 用终端界面，传 nil。
	UI *appapi.UI
	// ModesWorkdir 项目档位确认记录（trust.toml）用的目录；CLI 沿用 -dir 原值。
	ModesWorkdir string
	// Upload 传输层会挂 appapi.UploadHandler（网页服务、桌面端）：hello 报 upload 能力。
	Upload bool
	// Workspaces 工作区管理器（workspace.list / workspace.open）；nil = 不能切换。
	Workspaces appapi.Workspaces
}

// Built BuildApp 的结果。
type Built struct {
	API     *appapi.App
	DB      store.Store
	Model   string
	Perms   *permission.Sessions // 未装 permission 插件时为 nil
	Builder *assemble.Builder
	wired   *assemble.Wired
}

// Ready 跑 ready 点位（构造器就绪后再跑：如工作组补投会打开领导会话），返回各自的错误。
func (b *Built) Ready(ctx context.Context) []error { return b.wired.Ready(ctx) }

// MaxScanFiles workspace.files 最多遍历的文件数（大仓库里 @ 文件只看前这么多个）。
const MaxScanFiles = 20000

// BuildApp 接线（assemble.Wire：会话构造器与 [kernel] 宿主参数）、构造应用接口层，再启动后台循环。
// 不跑 ready，调用方在接好输出后调 Built.Ready。只跑一次性子命令（export / explain）的不调它，也就不起后台循环。
func (r *Runtime) BuildApp(o AppOptions) (*Built, error) {
	rt := r.Rt
	if rt.Models == nil {
		return nil, fmt.Errorf("profile 中没有 models 插件")
	}
	// 每次从 Profile 解析，不存在 Runtime 里：CLI 自己拼 Runtime（不走 Start），存字段的话它拿到的是零值——
	// 存工件阈值、工具超时、确认期限全为 0，3 行的结果也被当成"结果过大"（2026-11-08 v1 验收发现）。
	kern, err := assemble.ParseKernel(r.Profile.Kernel)
	if err != nil {
		return nil, fmt.Errorf("profile [kernel]: %w", err)
	}
	db := rt.Store.DB()
	var perms *permission.Sessions
	if v, ok := rt.Asm.Value(permission.Service); ok {
		perms, _ = v.(*permission.Sessions)
	}
	w, err := assemble.Wire(assemble.WireOptions{Asm: rt.Asm, Host: r.Host, DB: db, Blobs: rt.Store.Blobs(),
		Models: rt.Models, Workdir: r.Root, Kernel: kern, Definitions: rt.Defs,
		MaxSteps: o.MaxSteps, SleepAfter: r.sleepAfter, Hooks: o.Hooks})
	if err != nil {
		return nil, err
	}
	// 应用接口层：在构造器钩子上并入流式事件、把提交事件接到总线、档位变化提示发 notice。
	ao := appapi.Options{Host: r.Host, Bus: r.Bus, DB: db, Builder: w.Builder, Model: rt.Models.Model, Workdir: r.Root,
		Trusted: r.Trusted, Profile: r.Profile, Packages: r.Packages, UI: o.UI, Artifacts: rt.Store.Blobs()}
	root := r.Root
	ao.Profiles = Profiles{Base: r.BaseProfile, Extra: r.ExtraProfile, Root: root, Trusted: r.Trusted}
	ao.Models = ModelList{M: rt.Models}
	ao.Followups = FollowupsFrom(rt.Models)
	ao.Titler = TitlerFrom(rt.Models)
	ao.Upload = o.Upload
	ao.Workspaces = o.Workspaces
	ao.Files = func(ctx context.Context, q string, limit int) ([]wire.FileInfo, bool, error) { // 界面 @ 文件（workspace.files）
		hits, trunc, err := fs.ListFiles(ctx, root, q, limit, MaxScanFiles)
		out := make([]wire.FileInfo, len(hits))
		for i, h := range hits {
			out[i] = wire.FileInfo{Path: h.Path, Size: h.Size, MTime: h.MTime}
		}
		return out, trunc, err
	}
	for _, it := range rt.Asm.Items(assemble.PointOnSessionDelete) { // 删除会话时的插件清理回调（session.delete）
		c, ok := it.Impl.(store.Cleaner)
		if !ok {
			return nil, fmt.Errorf("插件 %s 的 %s 类型不符（%T）", it.Plugin, assemble.PointOnSessionDelete, it.Impl)
		}
		ao.Cleaners = append(ao.Cleaners, c)
	}
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
	if r.bgStop == nil {
		r.bgStop = w.Background()
	}
	return &Built{API: api, DB: db, Model: rt.Models.Model, Perms: perms, Builder: w.Builder, wired: w}, nil
}
