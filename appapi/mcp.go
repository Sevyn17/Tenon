package appapi

// MCP 管理页的协议命令（Mortise 客户端改进 M11，配置T14；docs/50 5.1）：mcp.list / test / save / remove / setEnabled。
// 列表、测试、启用记录由 mcp 插件的 service:mcp 经 adapters.MCP 注入（Options.MCP）；没装（或插件没开）时
// 全部返回 unsupported，hello 不报能力 mcp。
// 写定义文件（用户级 <home>/mcp.toml）由工作区管理器实现可选接口 MCPEditor（apphost.WorkspaceManager），
// 写完重建运行时（同 workspace.reload：全局范围、定义没变的服务实例由资源管理器保留，不重启）。
// mcp.list 只读，viewer 可调；其余要 operator（test 会起进程，其余写用户级文件）。

import (
	"context"
	"errors"
	"strings"

	"github.com/Sevyn17/tenon/appapi/wire"
)

// 本文件的命令（按文件登记，v2.go 的 registerCommands）。
func init() {
	registerCommands(map[string]handler{
		"mcp.list":       bind((*App).MCPList),
		"mcp.test":       bind((*App).MCPTest),
		"mcp.save":       bind((*App).MCPSave),
		"mcp.remove":     bind((*App).MCPRemove),
		"mcp.setEnabled": bind((*App).MCPSetEnabled),
	})
}

// CapMCP hello 能力：mcp.* 命令可用（mcp.save / remove 是否可用看 mcp.list 的 editable）。
const CapMCP = "mcp"

// MCP 本工作区的 MCP 服务状态、临时连接测试与启用记录（mcp 插件适配，见 adapters.MCP）。
type MCP interface {
	// List 全部服务（含停用的）与状态，Workspace、Notes；Editable 表示定义文件可写（没用 -config 显式文件）。
	// UserPath / ProjectPath / Revision 也由实现填。
	List(ctx context.Context) (wire.MCPListResult, error)
	// Test 用一份完整的草稿（env / headers 的值都已给出）临时连一次，返回工具列表。
	// 草稿不合法返回 *Error（invalid）；连不上、tools/list 失败返回其它错误（mcp.test 当作 ok = false）。
	Test(ctx context.Context, d wire.MCPServerDraft) ([]wire.MCPTool, error)
	// SetEnabled 写一条启用记录（scope 已规范成 MCPScope*）。没有这个服务返回 not_found。
	SetEnabled(ctx context.Context, name string, enabled bool, scope string) error
}

// MCPEditor Workspaces 的可选扩展：改用户级 mcp.toml 里的 [[server]]。实现方只写文件，重建由 appapi 调 Reloader。
// 返回的错误是 *Error 时原样交给客户端（conflict / invalid / not_found）。
type MCPEditor interface {
	// ResolveMCPDraft 按 wire.MCPServerDraft 的 env / headers 规则并上用户级同名定义的原值，返回每个键都有值的草稿，
	// 并校验字段。不写文件。
	ResolveMCPDraft(ctx context.Context, d wire.MCPServerDraft) (wire.MCPServerDraft, error)
	// SaveMCPServer 新增或替换同名定义；revision 非空时先比对文件当前版本。
	SaveMCPServer(ctx context.Context, d wire.MCPServerDraft, revision string) (MCPWrite, error)
	// RemoveMCPServer 删掉同名定义（没有 → not_found）。
	RemoveMCPServer(ctx context.Context, name, revision string) (MCPWrite, error)
}

// MCPWrite 一次写入的结果。Undo 把文件改回写入前的样子（重建失败时调用）。
type MCPWrite struct {
	Revision string
	// Changed为 false表示目标状态已经满足，没有写入，也不需要重建。
	Changed bool
	Undo    func() error
}

func (v *App) mcp() (MCP, error) {
	if v.o.MCP == nil {
		return nil, Errorf(wire.CodeUnsupported, "未开 MCP 插件（mcp）")
	}
	return v.o.MCP, nil
}

// mcpEditor 写定义要的两样：MCPEditor 与 Reloader。
func (v *App) mcpEditor() (MCPEditor, Reloader, error) {
	ed, ok := v.o.Workspaces.(MCPEditor)
	rl, ok2 := v.o.Workspaces.(Reloader)
	if v.o.Workspaces == nil || !ok || !ok2 {
		return nil, nil, Errorf(wire.CodeUnsupported, "这个应用不支持在界面里编辑 MCP 服务（直接改 mcp.toml 后重启）")
	}
	return ed, rl, nil
}

// MCPList 见 wire.MCPListResult。
func (v *App) MCPList(ctx context.Context, _ wire.Empty) (wire.MCPListResult, error) {
	m, err := v.mcp()
	if err != nil {
		return wire.MCPListResult{}, err
	}
	r, err := m.List(ctx)
	if err != nil {
		return wire.MCPListResult{}, err
	}
	if r.Servers == nil {
		r.Servers = []wire.MCPServerInfo{}
	}
	for i := range r.Servers { // 列表字段总是数组（前端不用判空）
		s := &r.Servers[i]
		if s.Args == nil {
			s.Args = []string{}
		}
		if s.Tools == nil {
			s.Tools = []wire.MCPTool{}
		}
		if s.EnvKeys == nil {
			s.EnvKeys = []string{}
		}
		if s.HeaderKeys == nil {
			s.HeaderKeys = []string{}
		}
	}
	if _, _, err := v.mcpEditor(); err != nil {
		r.Editable = false
	}
	return r, nil
}

// MCPTest 用草稿临时连一次。env / headers 里值为 null 的键取用户级同名定义的原值（有编辑器时）。
func (v *App) MCPTest(ctx context.Context, in wire.MCPTestArgs) (wire.MCPTestResult, error) {
	m, err := v.mcp()
	if err != nil {
		return wire.MCPTestResult{}, err
	}
	d := in.Server
	d.Name = strings.TrimSpace(d.Name)
	if ed, _, err := v.mcpEditor(); err == nil {
		if d, err = ed.ResolveMCPDraft(ctx, d); err != nil {
			return wire.MCPTestResult{}, AsError(err)
		}
	}
	tools, err := m.Test(ctx, d)
	if err != nil {
		var pe *Error
		if errors.As(err, &pe) {
			return wire.MCPTestResult{}, pe
		}
		return wire.MCPTestResult{OK: false, Error: strings.TrimPrefix(err.Error(), "mcp: "), Tools: []wire.MCPTool{}}, nil
	}
	if tools == nil {
		tools = []wire.MCPTool{}
	}
	return wire.MCPTestResult{OK: true, Tools: tools}, nil
}

// mcpWritable 名字为 name 的服务能不能经 save / remove 改：本工作区里它来自插件包、项目或 -config 文件时不能（invalid）。
func (v *App) mcpWritable(ctx context.Context, m MCP, name string) error {
	r, err := m.List(ctx)
	if err != nil {
		return err
	}
	if !r.Editable {
		return Errorf(wire.CodeUnsupported, "用了 -config 显式指定的 MCP 配置：用户级 mcp.toml 不生效，不能在界面里编辑")
	}
	for _, s := range r.Servers {
		if s.Name == name && s.Source != "user" {
			return invalid("服务 %s 来自 %s，只读（去对应的插件包、项目或配置文件里改）", name, s.Source)
		}
	}
	return nil
}

// mcpApply 写入 → 重建；有回合在跑时先拦下（not_idle，什么都不写）或 force 先取消。重建失败时调 Undo 改回文件。
func (v *App) mcpApply(ctx context.Context, rl Reloader, force bool, write func() (MCPWrite, error)) (wire.MCPEditResult, error) {
	ids, err := v.runningIDs(ctx)
	if err != nil {
		return wire.MCPEditResult{}, err
	}
	if len(ids) > 0 {
		if !force {
			e := Errorf(wire.CodeNotIdle, "还有 %d 个会话正在运行：改 MCP 服务要重建运行时，会中断它们（带 force 先取消再重建）", len(ids))
			e.Details = map[string]any{"running": len(ids), "sessions": ids}
			return wire.MCPEditResult{}, e
		}
		v.cancelAll(ctx, ids)
	}
	w, err := write()
	if err != nil {
		return wire.MCPEditResult{}, AsError(err)
	}
	if !w.Changed {
		return wire.MCPEditResult{Revision: w.Revision, Workspace: v.currentWorkspace()}, nil
	}
	undo := func(e *Error) *Error {
		if w.Undo != nil {
			if err := w.Undo(); err != nil {
				e.Message += "；mcp.toml 也没能改回：" + err.Error()
			}
		}
		return e
	}
	if rl == nil {
		return wire.MCPEditResult{Revision: w.Revision, Workspace: v.currentWorkspace()}, nil
	}
	ws, err := rl.Reload(ctx)
	if err != nil {
		var rv *ReloadReverted
		if errors.As(err, &rv) {
			e := Errorf(wire.CodeInvalid, "%s", rv.Error())
			e.Details = map[string]any{"reverted": true}
			return wire.MCPEditResult{}, undo(e)
		}
		return wire.MCPEditResult{}, undo(Errorf(wire.CodeInternal, "重建运行时失败：%v", err))
	}
	return wire.MCPEditResult{Revision: w.Revision, Reloaded: true, Workspace: ws}, nil
}

// MCPSave 见 wire.MCPSaveArgs。
func (v *App) MCPSave(ctx context.Context, in wire.MCPSaveArgs) (wire.MCPEditResult, error) {
	m, err := v.mcp()
	if err != nil {
		return wire.MCPEditResult{}, err
	}
	ed, rl, err := v.mcpEditor()
	if err != nil {
		return wire.MCPEditResult{}, err
	}
	d := in.Server
	d.Name = strings.TrimSpace(d.Name)
	if _, err := ed.ResolveMCPDraft(ctx, d); err != nil { // 先校验：不合法时不必等回合、不写文件
		return wire.MCPEditResult{}, AsError(err)
	}
	if err := v.mcpWritable(ctx, m, d.Name); err != nil {
		return wire.MCPEditResult{}, err
	}
	return v.mcpApply(ctx, rl, in.Force, func() (MCPWrite, error) {
		w, err := ed.SaveMCPServer(ctx, d, in.Revision)
		w.Changed = err == nil
		return w, err
	})
}

// MCPRemove 见 wire.MCPRemoveArgs。
func (v *App) MCPRemove(ctx context.Context, in wire.MCPRemoveArgs) (wire.MCPEditResult, error) {
	m, err := v.mcp()
	if err != nil {
		return wire.MCPEditResult{}, err
	}
	ed, rl, err := v.mcpEditor()
	if err != nil {
		return wire.MCPEditResult{}, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return wire.MCPEditResult{}, invalid("缺少 name")
	}
	if err := v.mcpWritable(ctx, m, name); err != nil {
		return wire.MCPEditResult{}, err
	}
	return v.mcpApply(ctx, rl, in.Force, func() (MCPWrite, error) {
		w, err := ed.RemoveMCPServer(ctx, name, in.Revision)
		w.Changed = err == nil
		return w, err
	})
}

// MCPSetEnabled 见 wire.MCPSetEnabledArgs。应用不能重建时只写记录（reloaded = false）。
// 重建失败时记录不改回（启用记录只决定连哪些服务，单个服务连不上不会让重建失败）。
func (v *App) MCPSetEnabled(ctx context.Context, in wire.MCPSetEnabledArgs) (wire.MCPEditResult, error) {
	m, err := v.mcp()
	if err != nil {
		return wire.MCPEditResult{}, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return wire.MCPEditResult{}, invalid("缺少 name")
	}
	scope := in.Scope
	switch scope {
	case "":
		scope = wire.MCPScopeGlobal
	case wire.MCPScopeGlobal, wire.MCPScopeWorkspace:
	default:
		return wire.MCPEditResult{}, invalid("scope 只能是 %s / %s，得到 %q", wire.MCPScopeGlobal, wire.MCPScopeWorkspace, in.Scope)
	}
	var rl Reloader
	if r, ok := v.o.Workspaces.(Reloader); ok && v.o.Workspaces != nil {
		rl = r
	}
	return v.mcpApply(ctx, rl, in.Force, func() (MCPWrite, error) {
		before, err := m.List(ctx)
		if err != nil {
			return MCPWrite{}, err
		}
		for _, server := range before.Servers {
			if server.Name == name && server.Enabled == in.Enabled {
				return MCPWrite{Revision: before.Revision, Changed: false}, nil
			}
		}
		if err := m.SetEnabled(ctx, name, in.Enabled, scope); err != nil {
			return MCPWrite{}, err
		}
		after, err := m.List(ctx)
		if err != nil {
			return MCPWrite{}, err
		}
		return MCPWrite{Revision: after.Revision, Changed: true}, nil
	})
}
