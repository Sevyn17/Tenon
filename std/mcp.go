package std

// MCP 管理页（客户端改进 M11，配置T14）：把 mcp 插件的 service:mcp（*mcp.ServiceAPI）接成 appapi.MCP。
// 写定义文件不在这里：apphost.WorkspaceManager 实现 appapi.MCPEditor。

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/Sevyn17/tenon/appapi"
	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/modules/tools/mcp"
	"github.com/Sevyn17/tenon/runtime/store"
)

// MCP appapi.MCP 的 mcp 插件实现。
type MCP struct{ S *mcp.ServiceAPI }

// descMax mcp.list 里工具描述的上限（字符）：只给一句话，全文在模型侧。
const descMax = 160

// oneLine 描述的第一行，过长截断。
func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if r := []rune(s); len(r) > descMax {
		s = string(r[:descMax]) + "…"
	}
	return s
}

func mcpTools(ts []mcp.ToolInfo) []wire.MCPTool {
	out := make([]wire.MCPTool, 0, len(ts))
	for _, t := range ts {
		out = append(out, wire.MCPTool{Name: t.Name, Public: t.Public, Description: oneLine(t.Description)})
	}
	return out
}

// mcpState 列表里的状态：按最新启用记录。记录说启用、运行时没连（刚改了记录、还没重建）= starting。
func mcpState(s mcp.ServerStatus) string {
	switch {
	case !s.Enabled:
		return wire.MCPStateDisabled
	case s.State == mcp.StateConnected:
		return wire.MCPStateConnected
	case s.State == mcp.StateFailed:
		return wire.MCPStateFailed
	default:
		return wire.MCPStateStarting
	}
}

// List mcp.list。
func (a MCP) List(context.Context) (wire.MCPListResult, error) {
	out := wire.MCPListResult{Servers: []wire.MCPServerInfo{}, UserPath: mcp.UserConfigPath(), Editable: !a.S.Explicit()}
	if wd := a.S.Workdir(); wd != "" {
		out.ProjectPath = mcp.ProjectConfigPath(wd)
	}
	if en := a.S.Enabled(); en != nil {
		out.Workspace = en.Workspace
	}
	// 用户级定义的原值（cwd 未经项目根解析），给编辑表单回填。
	raw := map[string]mcp.Server{}
	if out.UserPath != "" {
		rev, err := mcp.FileRevision(out.UserPath)
		if err != nil {
			out.Notes = append(out.Notes, "读 "+out.UserPath+"："+err.Error())
		}
		out.Revision = rev
		if c, err := mcp.LoadConfig(out.UserPath); err == nil {
			for _, s := range c.Server {
				raw[s.Name] = s
			}
		}
	}
	for _, s := range a.S.Servers() {
		info := wire.MCPServerInfo{Name: s.Name, Source: s.Source, Transport: s.Transport, Command: s.Command,
			Args: append([]string{}, s.Args...), URL: s.URL, Cwd: s.Cwd, EffectiveCwd: s.Cwd, Timeout: s.Timeout,
			Scope: s.Scope, Trust: s.Trust, Sandbox: s.Sandbox, Enabled: s.Enabled, DisabledScope: s.DisabledScope,
			State: mcpState(s), Tools: []wire.MCPTool{}, EnvKeys: append([]string{}, s.EnvKeys...),
			HeaderKeys: append([]string{}, s.HeaderKeys...), Readonly: s.Source != mcp.SourceUser || a.S.Explicit()}
		if s.Source == mcp.SourceUser {
			if r, ok := raw[s.Name]; ok {
				info.Cwd = r.Cwd
			}
		}
		if info.State == wire.MCPStateFailed {
			info.Error = strings.TrimPrefix(s.Error, "mcp: ")
		}
		if info.State == wire.MCPStateConnected {
			info.Tools = mcpTools(s.Tools)
		}
		out.Servers = append(out.Servers, info)
	}
	switch {
	case a.S.Explicit():
		out.Notes = append(out.Notes, "用了 -config 显式指定的 MCP 配置：只读这个文件，用户级与项目 mcp.toml、启用记录都不生效，也不能在界面里编辑")
	case out.ProjectPath != "" && !a.S.ProjectTrusted():
		if st, err := os.Stat(out.ProjectPath); err == nil && st.Mode().IsRegular() {
			out.Notes = append(out.Notes, fmt.Sprintf("项目未受信：%s 里定义的服务没有列出，也不会启动（信任项目后生效）", out.ProjectPath))
		}
	}
	return out, nil
}

// DraftServer 把一份 env / headers 值都已给出的草稿转成 mcp.Server 并校验（mcp.test、apphost 写文件前共用）。
// 不合法返回 invalid 的 *appapi.Error。
func DraftServer(d wire.MCPServerDraft) (mcp.Server, error) {
	bad := func(format string, args ...any) (mcp.Server, error) {
		return mcp.Server{}, appapi.Errorf(wire.CodeInvalid, format, args...)
	}
	d.Name = strings.TrimSpace(d.Name)
	if d.Name == "" {
		return bad("缺少服务名 name")
	}
	switch d.Transport {
	case "":
	case wire.MCPTransportStdio:
		if d.Command == "" || d.URL != "" {
			return bad("服务 %s：transport = stdio 要填 command、不能填 url", d.Name)
		}
	case wire.MCPTransportHTTP:
		if d.URL == "" || d.Command != "" {
			return bad("服务 %s：transport = http 要填 url、不能填 command", d.Name)
		}
	default:
		return bad("服务 %s：transport 只能是 %s / %s，得到 %q", d.Name, wire.MCPTransportStdio, wire.MCPTransportHTTP, d.Transport)
	}
	if d.Timeout < 0 {
		return bad("服务 %s：timeout 不能是负数", d.Name)
	}
	s := mcp.Server{Name: d.Name, Command: d.Command, Args: d.Args, Cwd: d.Cwd, URL: d.URL, Timeout: d.Timeout,
		Trust: d.Trust, Sandbox: d.Sandbox, Scope: d.Scope, Source: mcp.SourceUser}
	var err error
	if s.Env, err = draftValues(d.Name, "env", d.Env); err != nil {
		return mcp.Server{}, err
	}
	if s.Headers, err = draftValues(d.Name, "headers", d.Headers); err != nil {
		return mcp.Server{}, err
	}
	if err := mcp.CheckServer(s); err != nil {
		return bad("%s", err.Error())
	}
	return s, nil
}

func draftValues(name, field string, m map[string]*string) (map[string]string, error) {
	if len(m) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if strings.TrimSpace(k) == "" {
			return nil, appapi.Errorf(wire.CodeInvalid, "服务 %s 的 %s 有空键名", name, field)
		}
		if v == nil {
			return nil, appapi.Errorf(wire.CodeInvalid, "服务 %s 的 %s.%s 没有值（null 只表示保留已保存的值，这里没有可保留的）", name, field, k)
		}
		out[k] = *v
	}
	return out, nil
}

// Test mcp.test：临时连一次，不经资源管理器、不注册工具。
func (a MCP) Test(ctx context.Context, d wire.MCPServerDraft) ([]wire.MCPTool, error) {
	s, err := DraftServer(d)
	if err != nil {
		return nil, err
	}
	ts, err := a.S.Test(ctx, s)
	if err != nil {
		return nil, err
	}
	return mcpTools(ts), nil
}

// SetEnabled mcp.setEnabled：只认本工作区列表里有的服务（not_found）。
func (a MCP) SetEnabled(_ context.Context, name string, enabled bool, scope string) error {
	en := a.S.Enabled()
	if en == nil {
		return appapi.Errorf(wire.CodeUnsupported, "用了 -config 显式指定的 MCP 配置：不看启用记录")
	}
	found := false
	for _, s := range a.S.Servers() {
		if s.Name == name {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("%w: 没有 MCP 服务 %s", store.ErrNotFound, name)
	}
	if err := en.Set(name, enabled, scope); err != nil {
		return appapi.Errorf(wire.CodeInvalid, "%s", err.Error())
	}
	return nil
}
