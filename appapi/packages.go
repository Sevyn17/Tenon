package appapi

// 插件包管理（Mortise 客户端改进 M11 配置T22，spike-config 第六节）：packages.preview / install / remove / setEnabled，
// 以及 packages.list 补的 enabled、dir、risks、managed、error 字段与 plugins.list 的 risks（T24）。
// 读写磁盘由工作区管理器实现可选接口 PackageManager（apphost.WorkspaceManager）；写完重建运行时（同 workspace.reload），
// 重建失败时调 Undo 把磁盘改回。管理器实现了 PackageManager 且能重建（Reloader）时 hello 报能力 packages.manage。
// 只管用户级包（<home>/plugins）；项目级包只读。packages.list 只读，viewer 可调；其余只给 operator
// （preview 会读服务端本机任意路径、其余写用户级目录并重建）。

import (
	"context"
	"errors"
	"strings"

	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/pkgs"
)

// 本文件的命令（按文件登记，v2.go 的 registerCommands）。
func init() {
	registerCommands(map[string]handler{
		"packages.list":       bind((*App).PackagesList),
		"packages.preview":    bind((*App).PackagesPreview),
		"packages.install":    bind((*App).PackagesInstall),
		"packages.remove":     bind((*App).PackagesRemove),
		"packages.setEnabled": bind((*App).PackagesSetEnabled),
	})
}

// CapPackagesManage hello 能力：packages.preview / install / remove / setEnabled 可用，packages.list 带 enabled 等字段。
const CapPackagesManage = "packages.manage"

// PackageManager Workspaces 的可选扩展：用户级插件包的预览、安装、卸载、启停。实现方只改磁盘，重建由 appapi 调 Reloader。
// 返回的错误是 *Error 时原样交给客户端（invalid / not_found）。
type PackageManager interface {
	// UserPackages 用户级插件包目录里的全部包（含停用的、清单坏了的），每项带 Enabled、Dir、Error。
	UserPackages(ctx context.Context) ([]wire.PackageInfo, error)
	// PreviewPackage 只读解析来源（本地目录或 .zip），不安装。来源或清单不合法 → invalid。
	PreviewPackage(ctx context.Context, source string) (wire.PackagesPreviewResult, error)
	// InstallPackage 装到用户级目录；同名旧包挪进 .trash。
	InstallPackage(ctx context.Context, source string) (PackageWrite, error)
	// RemovePackage 把用户级包挪进 .trash。项目包 → invalid；没有 → not_found。
	RemovePackage(ctx context.Context, name string) (PackageWrite, error)
	// SetPackageEnabled 写启停记录；状态没变时 Changed = false、不用重建。
	SetPackageEnabled(ctx context.Context, name string, enabled bool) (PackageWrite, error)
}

// PackageWrite 一次改动的结果。Undo 把磁盘改回改动前（重建失败时调用）；Changed = false 时不重建。
type PackageWrite struct {
	Name    string
	Trashed string
	Changed bool
	Undo    func() error
}

func (v *App) packageManager() (PackageManager, Reloader, error) {
	pm, ok := v.o.Workspaces.(PackageManager)
	rl, ok2 := v.o.Workspaces.(Reloader)
	if v.o.Workspaces == nil || !ok || !ok2 {
		return nil, nil, Errorf(wire.CodeUnsupported, "this app does not support managing packages (put them in the user plugins directory and restart)")
	}
	return pm, rl, nil
}

// PackagesList 已加载的插件包（用户级在前、项目级在后）；支持 packages.manage 时再补上用户级目录里没加载的包
// （停用的、清单坏了的、被同名项目包覆盖的），它们 enabled = false 或带 error，不带界面贡献。
func (v *App) PackagesList(ctx context.Context, _ wire.Empty) (wire.PackagesListResult, error) {
	pm, _, merr := v.packageManager()
	out := wire.PackagesListResult{Packages: []wire.PackageInfo{}}
	loaded := map[string]string{} // 名字 → scope
	for _, k := range v.o.Packages {
		pi := wire.PackageInfo{Name: k.Name, Version: k.Version, Description: k.Description,
			Scope: k.Scope, Source: k.File, Contents: k.Contents(), Enabled: true, Dir: k.Dir, Risks: k.Risks(),
			Managed: merr == nil && k.Scope == pkgs.ScopeUser}
		for _, u := range k.UISlots() {
			ws := wire.UISlot{ID: u.ID, Slot: u.Slot, Title: u.Title, Order: u.Order, Requires: u.Requires, View: u.View}
			if u.Data != nil {
				ws.Data = &wire.UIData{Cmd: u.Data.Cmd, Args: u.Data.Args}
			}
			pi.UI = append(pi.UI, ws)
		}
		loaded[k.Name] = k.Scope
		out.Packages = append(out.Packages, pi)
	}
	if merr != nil {
		return out, nil
	}
	us, err := pm.UserPackages(ctx)
	if err != nil {
		return wire.PackagesListResult{}, AsError(err)
	}
	for _, u := range us {
		if loaded[u.Name] == pkgs.ScopeUser {
			continue
		}
		u.Scope, u.Managed, u.UI = pkgs.ScopeUser, true, nil
		if u.Risks == nil {
			u.Risks = []string{}
		}
		if u.Enabled && u.Error == "" {
			if loaded[u.Name] == pkgs.ScopeProject {
				u.Error = "shadowed by a project package with the same name; not loaded"
			} else {
				u.Error = "not loaded (takes effect after a runtime rebuild)"
			}
		}
		if u.Error != "" {
			u.Enabled = false // 没生效；启停记录本身看不出来时以 error 说明原因
		}
		out.Packages = append(out.Packages, u)
	}
	return out, nil
}

// PackagesPreview 见 wire.PackagesPreviewArgs。
func (v *App) PackagesPreview(ctx context.Context, in wire.PackagesPreviewArgs) (wire.PackagesPreviewResult, error) {
	pm, _, err := v.packageManager()
	if err != nil {
		return wire.PackagesPreviewResult{}, err
	}
	if strings.TrimSpace(in.Source) == "" {
		return wire.PackagesPreviewResult{}, invalid("missing source")
	}
	r, err := pm.PreviewPackage(ctx, strings.TrimSpace(in.Source))
	if err != nil {
		return wire.PackagesPreviewResult{}, AsError(err)
	}
	fillPreview(&r)
	return r, nil
}

// fillPreview 列表字段总是数组（前端不用判空）。
func fillPreview(r *wire.PackagesPreviewResult) {
	if r.MCPServers == nil {
		r.MCPServers = []wire.TrustMCPServer{}
	}
	if r.Hooks == nil {
		r.Hooks = []wire.TrustHook{}
	}
	for _, p := range []*[]string{&r.Skills, &r.Prompts, &r.Agents, &r.UICommands, &r.Risks} {
		if *p == nil {
			*p = []string{}
		}
	}
	if r.Config == nil {
		r.Config = []wire.PackageConfig{}
	}
	if r.UI == nil {
		r.UI = []wire.UISlot{}
	}
}

// PackagesInstall 见 wire.PackagesInstallArgs。先预览一遍（来源不合法时不等回合、不动磁盘）。
func (v *App) PackagesInstall(ctx context.Context, in wire.PackagesInstallArgs) (wire.PackagesEditResult, error) {
	pm, rl, err := v.packageManager()
	if err != nil {
		return wire.PackagesEditResult{}, err
	}
	src := strings.TrimSpace(in.Source)
	if src == "" {
		return wire.PackagesEditResult{}, invalid("missing source")
	}
	if _, err := pm.PreviewPackage(ctx, src); err != nil {
		return wire.PackagesEditResult{}, AsError(err)
	}
	return v.packagesApply(ctx, rl, in.Force, func() (PackageWrite, error) { return pm.InstallPackage(ctx, src) })
}

// PackagesRemove 见 wire.PackagesRemoveArgs。
func (v *App) PackagesRemove(ctx context.Context, in wire.PackagesRemoveArgs) (wire.PackagesEditResult, error) {
	pm, rl, err := v.packageManager()
	if err != nil {
		return wire.PackagesEditResult{}, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return wire.PackagesEditResult{}, invalid("missing name")
	}
	return v.packagesApply(ctx, rl, in.Force, func() (PackageWrite, error) { return pm.RemovePackage(ctx, name) })
}

// PackagesSetEnabled 见 wire.PackagesSetEnabledArgs。
func (v *App) PackagesSetEnabled(ctx context.Context, in wire.PackagesSetEnabledArgs) (wire.PackagesEditResult, error) {
	pm, rl, err := v.packageManager()
	if err != nil {
		return wire.PackagesEditResult{}, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return wire.PackagesEditResult{}, invalid("missing name")
	}
	return v.packagesApply(ctx, rl, in.Force, func() (PackageWrite, error) { return pm.SetPackageEnabled(ctx, name, in.Enabled) })
}

// packagesApply 同 mcpApply：有回合在跑时先拦下（not_idle，什么都不动）或 force 先取消；改磁盘 → 重建；
// 重建失败时调 Undo 改回。改动没生效（Changed = false）时不重建。
func (v *App) packagesApply(ctx context.Context, rl Reloader, force bool, write func() (PackageWrite, error)) (wire.PackagesEditResult, error) {
	ids, err := v.runningIDs(ctx)
	if err != nil {
		return wire.PackagesEditResult{}, err
	}
	if len(ids) > 0 {
		if !force {
			e := Errorf(wire.CodeNotIdle, "%d session(s) running: changing packages rebuilds the runtime and interrupts them (use force to cancel and rebuild)", len(ids))
			e.Details = map[string]any{"running": len(ids), "sessions": ids}
			return wire.PackagesEditResult{}, e
		}
		v.cancelAll(ctx, ids)
	}
	w, err := write()
	if err != nil {
		return wire.PackagesEditResult{}, AsError(err)
	}
	res := wire.PackagesEditResult{Name: w.Name, Trashed: w.Trashed}
	if !w.Changed {
		res.Workspace = v.currentWorkspace()
		return res, nil
	}
	undo := func(e *Error) *Error {
		if w.Undo != nil {
			if err := w.Undo(); err != nil {
				e.Message += "; the packages directory could not be restored either: " + err.Error()
			}
		}
		return e
	}
	ws, err := rl.Reload(ctx)
	if err != nil {
		var rv *ReloadReverted
		if errors.As(err, &rv) {
			e := Errorf(wire.CodeInvalid, "%s", rv.Error())
			e.Details = map[string]any{"reverted": true}
			return wire.PackagesEditResult{}, undo(e)
		}
		return wire.PackagesEditResult{}, undo(Errorf(wire.CodeInternal, "failed to rebuild runtime: %v", err))
	}
	res.Reloaded, res.Workspace = true, ws
	return res, nil
}

// PluginRisks 打开插件前要确认的风险（客户端改进 M11 T24），由 effects 推断：exec → exec，network → network，
// system_prompt / rewrite_result → prompt；tools 不单列（加的工具仍受档位与沙箱约束）。按 wire.Risk* 的顺序去重。
func PluginRisks(effects []string) []string {
	var rs []string
	for _, e := range effects {
		switch e {
		case component.EffectExec:
			rs = append(rs, wire.RiskExec)
		case component.EffectNetwork:
			rs = append(rs, wire.RiskNetwork)
		case component.EffectSystemPrompt, component.EffectRewriteResult:
			rs = append(rs, wire.RiskPrompt)
		}
	}
	return pkgs.SortRisks(rs)
}
