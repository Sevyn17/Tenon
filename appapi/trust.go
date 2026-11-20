package appapi

// 项目信任（客户端改进 M11 配置T13，spike-config 第四节）：workspace.trustPreview 列出当前工作区受信后会生效的
// 项目配置（MCP 服务、钩子、放宽的档位、插件包、profile 插件设置）和信任范围；workspace.trust 写信任记录后
// 重建运行时（同 workspace.reload）。受信后项目 .<数据目录>/mcp.toml 直接生效（docs/50 §5.1）。
// 工作区管理器实现可选接口 Truster、且能重建（Reloader）才可用，否则 unsupported；可用时 hello 报能力 workspace.trust。
// trustPreview 只读，viewer 可调；trust 写用户级信任记录，只给 operator（conn.go 的 viewerCommands）。

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/Sevyn17/tenon/appapi/wire"
)

// CapWorkspaceTrust hello 能力：workspace.trustPreview / workspace.trust 可用。
const CapWorkspaceTrust = "workspace.trust"

// Truster Workspaces 的可选扩展：项目信任的预览与写入。实现方只写记录，重建由 appapi 调 Reloader.Reload。
type Truster interface {
	// TrustPreview 当前工作区受信后会生效的项目配置（只读，不启动任何东西）与当前信任状态。
	TrustPreview(ctx context.Context) (wire.WorkspaceTrustPreviewResult, error)
	// SetTrust 在当前工作区的信任键上写一条记录（level 为空 = 删除），返回变化前后的情况。
	SetTrust(ctx context.Context, level string) (TrustChange, error)
}

// TrustChange SetTrust 的结果。
type TrustChange struct {
	Key  string // 信任键（git 根或工作区目录）
	Prev string // 键上原来的记录；"" = 没有（撤回时原样写回）
	// Before 当前运行时是否受信；After 按新记录重建后是否受信。两者相同就不用重建。
	Before, After bool
}

func (v *App) truster() (Truster, Reloader, error) {
	if v.o.Workspaces == nil {
		return nil, nil, Errorf(wire.CodeUnsupported, "这个应用没有工作区管理器，不能在界面里信任项目")
	}
	t, ok := v.o.Workspaces.(Truster)
	rl, ok2 := v.o.Workspaces.(Reloader)
	if !ok || !ok2 {
		return nil, nil, Errorf(wire.CodeUnsupported, "这个应用不支持在界面里信任项目")
	}
	return t, rl, nil
}

// WorkspaceTrustPreview 见 wire.WorkspaceTrustPreviewResult。
func (v *App) WorkspaceTrustPreview(ctx context.Context, _ wire.Empty) (wire.WorkspaceTrustPreviewResult, error) {
	t, _, err := v.truster()
	if err != nil {
		return wire.WorkspaceTrustPreviewResult{}, err
	}
	out, err := t.TrustPreview(ctx)
	if err != nil {
		return wire.WorkspaceTrustPreviewResult{}, Errorf(wire.CodeInternal, "读项目配置失败：%v", err)
	}
	// 列表字段总是数组（前端不用判空）。
	if out.Files == nil {
		out.Files = []string{}
	}
	if out.MCPServers == nil {
		out.MCPServers = []wire.TrustMCPServer{}
	}
	if out.Hooks == nil {
		out.Hooks = []wire.TrustHook{}
	}
	if out.WidenedModes == nil {
		out.WidenedModes = []wire.TrustMode{}
	}
	if out.Packages == nil {
		out.Packages = []wire.TrustPackage{}
	}
	if out.Plugins == nil {
		out.Plugins = []wire.TrustPluginSetting{}
	}
	return out, nil
}

// WorkspaceTrust 写信任记录 → 生效的信任状态变了才重建（有回合在跑时同 workspace.reload：not_idle 或 force 先取消）。
// 不能重建（回合在跑且没带 force）或重建失败时，信任记录改回原样。
func (v *App) WorkspaceTrust(ctx context.Context, in wire.WorkspaceTrustArgs) (wire.WorkspaceTrustResult, error) {
	t, rl, err := v.truster()
	if err != nil {
		return wire.WorkspaceTrustResult{}, err
	}
	switch in.Level {
	case wire.TrustTrusted, wire.TrustUntrusted, "":
	default:
		return wire.WorkspaceTrustResult{}, invalid("level 应为 trusted / untrusted 或空（撤销记录），得到 %q", in.Level)
	}
	ch, err := t.SetTrust(ctx, in.Level)
	if err != nil {
		return wire.WorkspaceTrustResult{}, Errorf(wire.CodeInternal, "保存信任记录失败：%v", err)
	}
	res := wire.WorkspaceTrustResult{Key: ch.Key, Level: in.Level}
	if ch.Before == ch.After { // 生效的信任没变（如启动参数强制受信）：只写记录
		res.Workspace = v.currentWorkspace()
		return res, nil
	}
	undo := func(e *Error) *Error {
		if _, err := t.SetTrust(ctx, ch.Prev); err != nil {
			e.Message += "；信任记录也没能改回：" + err.Error()
		}
		return e
	}
	ids, err := v.runningIDs(ctx)
	if err != nil {
		return wire.WorkspaceTrustResult{}, undo(AsError(err))
	}
	if len(ids) > 0 {
		if !in.Force {
			e := Errorf(wire.CodeNotIdle, "还有 %d 个会话正在运行：改信任要重建运行时，会中断它们（带 force 先取消再重建）", len(ids))
			e.Details = map[string]any{"running": len(ids), "sessions": ids}
			return wire.WorkspaceTrustResult{}, undo(e)
		}
		v.cancelAll(ctx, ids)
	}
	ws, err := rl.Reload(ctx)
	if err != nil {
		var rv *ReloadReverted
		if errors.As(err, &rv) {
			e := Errorf(wire.CodeInvalid, "%s", rv.Error())
			e.Details = map[string]any{"reverted": true}
			return wire.WorkspaceTrustResult{}, undo(e)
		}
		return wire.WorkspaceTrustResult{}, undo(Errorf(wire.CodeInternal, "重建运行时失败：%v", err))
	}
	res.Workspace, res.Reloaded = ws, true
	return res, nil
}

// currentWorkspace 管理器列表里的当前工作区；列不出来时按本 App 的目录拼一个。
func (v *App) currentWorkspace() wire.WorkspaceInfo {
	if v.o.Workspaces != nil {
		for _, w := range v.o.Workspaces.List() {
			if w.Current {
				return w
			}
		}
	}
	return wire.WorkspaceInfo{Path: v.o.Workdir, Name: filepath.Base(v.o.Workdir), Trusted: v.o.Trusted, Current: true}
}
