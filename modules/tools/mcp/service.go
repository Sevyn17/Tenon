package mcp

// service:mcp（Mortise 客户端改进 M11，配置T14）：MCP 管理页的协议命令（mcp.list / test / save / remove，下一段接）
// 经它取本工作区的服务状态、启用记录与实例列表。写定义文件（mcp.toml）由 apphost 做，本服务只读。

import (
	"context"
	"fmt"
	"sort"

	"github.com/Sevyn17/tenon/runtime/resmgr"
)

// 服务状态（ServerStatus.State）。
const (
	StateConnected = "connected"
	StateFailed    = "failed"
	StateDisabled  = "disabled" // 本工作区没启用
)

// ToolInfo 服务提供的一个工具。
type ToolInfo struct {
	Name        string // 服务端原名
	Public      string // 模型侧名字 mcp__<服务>__<工具>
	Description string
}

// ServerStatus 一个服务在本工作区的状态。env / headers 只给键名，不回显值（可能是密钥）。
type ServerStatus struct {
	Name       string
	Source     string // user / project / package:<包名> / config
	Transport  string // stdio / http
	Command    string
	Args       []string
	URL        string
	Cwd        string
	Timeout    int
	Trust      string // 空 = ask
	Sandbox    string
	Scope      string // global / workspace
	EnvKeys    []string
	HeaderKeys []string
	// Enabled 本工作区的启用记录；DisabledScope 是让它停用的那条记录的范围（global / workspace）。
	Enabled       bool
	DisabledScope string
	State         string
	Error         string
	Tools         []ToolInfo
	// Instance 连上时所用实例的隔离键（与 Service.Instances 对得上）。
	Instance resmgr.Key
}

func (p *plug) newStatus(s Server, state string) *ServerStatus {
	st := &ServerStatus{Name: s.Name, Source: s.Source, Command: s.Command, Args: append([]string(nil), s.Args...),
		URL: s.URL, Cwd: s.Cwd, Timeout: s.Timeout, Trust: s.Trust, Sandbox: s.Sandbox, Scope: s.Scope,
		EnvKeys: keys(s.Env), HeaderKeys: keys(s.Headers), Enabled: true, State: state}
	st.Transport = "http"
	if s.Command != "" {
		st.Transport = "stdio"
	}
	if s.noExpand { // 插件包：${TENON_PLUGIN_DIR} 是插件自己加的，不算用户写的键
		st.EnvKeys = without(st.EnvKeys, "TENON_PLUGIN_DIR")
	}
	return st
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func without(xs []string, x string) []string {
	out := xs[:0:0]
	for _, v := range xs {
		if v != x {
			out = append(out, v)
		}
	}
	return out
}

// ServiceAPI service:mcp 的值。
type ServiceAPI struct{ p *plug }

// Servers 本工作区可见的全部服务定义与状态（含停用的），按定义顺序：用户级、项目、插件包。
func (s *ServiceAPI) Servers() []ServerStatus {
	p := s.p
	var en map[string]EnabledStatus
	if p.enabled != nil {
		names := make([]string, len(p.all))
		for i, sv := range p.all {
			names[i] = sv.Name
		}
		en, _ = p.enabled.Statuses(names) // 按最新记录显示；生效的连接要重建运行时才变
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]ServerStatus, 0, len(p.all))
	for _, sv := range p.all {
		st := p.status[sv.Name]
		if st == nil { // Start 之前
			st = p.newStatus(sv, StateDisabled)
		}
		c := *st
		c.Tools = append([]ToolInfo(nil), st.Tools...)
		if e, ok := en[sv.Name]; ok {
			c.Enabled, c.DisabledScope = e.Enabled, e.Scope
		}
		out = append(out, c)
	}
	return out
}

// Enabled 本工作区的启用记录（写 mcp-enabled.toml）；用 -config 显式文件时为 nil（不看启用记录）。
// 改了记录要重建运行时（workspace.reload）才生效。
func (s *ServiceAPI) Enabled() *Enabled { return s.p.enabled }

// Instances 资源管理器里当前在跑的 MCP 实例（含别的工作区取的全局实例）。没有应用级资源管理器时只有本运行时的。
func (s *ServiceAPI) Instances() []resmgr.Instance {
	var m *resmgr.Manager
	if s.p.bound != nil {
		m = s.p.bound.Manager()
	} else {
		m = s.p.local
	}
	if m == nil {
		return nil
	}
	var out []resmgr.Instance
	for _, in := range m.List() {
		if in.Key.Kind == ResourceKind {
			out = append(out, in)
		}
	}
	return out
}

// Test 临时连一次 sv（可以是还没保存的草稿），拉 tools/list 后断开；不经资源管理器、不注册工具。
// 校验同 mcp.toml；stdio 服务按本工作区的沙箱设置放进沙箱。
func (s *ServiceAPI) Test(ctx context.Context, sv Server) ([]ToolInfo, error) {
	if err := checkServers([]Server{sv}); err != nil {
		return nil, fmt.Errorf("mcp: %w", err)
	}
	p := s.p
	if sv.Scope == "" {
		sv.Scope = ScopeWorkspace
	}
	if sv.Command != "" && sv.Scope == ScopeWorkspace && sv.Cwd == "" && p.workdir != "" {
		sv.Cwd = p.workdir
	}
	c, err := Connect(ctx, p.cfg.confined(sv))
	if err != nil {
		return nil, err
	}
	defer c.Close()
	rts, err := c.ListTools(ctx)
	if err != nil {
		return nil, fmt.Errorf("mcp: %s tools/list 失败：%w", sv.Name, err)
	}
	out := make([]ToolInfo, 0, len(rts))
	for _, rt := range rts {
		out = append(out, ToolInfo{Name: rt.Name, Public: PublicName(sv.Name, rt.Name), Description: rt.Description})
	}
	return out, nil
}
