package mcp

// 插件登记（thin-kernel-plan §2.2）：MCP 客户端。运行时启动时取连接，工具列表此后不变（T3）。
//
// 定义（Mortise 客户端改进 M11，docs/50 5.1 决定 5；推翻 18 方案第七节「不加用户级」）：
//   - 用户级 <home>/mcp.toml：全局定义，对所有工作区可见；服务可写 scope = "global"（所有工作区共用一份进程，
//     切换工作区不重启）或 "workspace"（缺省，每个工作区各起一份，stdio 服务工作目录缺省为项目根）。
//   - 项目 <dir>/<数据目录>/mcp.toml：项目受信后直接生效，作为该工作区的定义，一律按工作区分配；
//     与用户级重名时项目覆盖并提示。项目不受信不读。
//   - 插件包的 [[server]] 走 pkgs.Service：用户级包同用户级定义，项目级包按工作区；与上面重名报错。
//   - 命令行 -config 给的显式路径直接读文件，不看分层。
//
// 使用分开：每个工作区按名字启用哪些服务记在用户级 mcp-enabled.toml（见 enabled.go），停用的服务不连、不出工具。
// 实例经统一资源管理器（runtime/resmgr，apphost 注入 service:resources）按 (定义指纹, 范围) 分配；
// 没注入时（嵌入、CLI 一次性运行）插件自己建一个只活到本运行时结束的管理器，行为与原来相同。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/Sevyn17/tenon/runtime/appid"
	"github.com/Sevyn17/tenon/runtime/assemble"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/pkgs"
	"github.com/Sevyn17/tenon/runtime/resmgr"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// Service 本插件提供的服务（值为 *Service），MCP 管理页的协议命令经它取状态。
const Service = "service:mcp"

// ResourceKind 资源管理器里 MCP 实例的类别（resmgr.Key.Kind）。
const ResourceKind = "mcp"

// ConfigFile 用户级与项目级定义的文件名。
const ConfigFile = "mcp.toml"

// UserConfigPath 用户级定义文件 <home>/mcp.toml；取不到用户目录返回空。
func UserConfigPath() string {
	if h := appid.Home(); h != "" {
		return filepath.Join(h, ConfigFile)
	}
	return ""
}

// ProjectConfigPath 工作区 root 的项目定义文件 <root>/<数据目录>/mcp.toml。
func ProjectConfigPath(root string) string { return filepath.Join(appid.Project(root), ConfigFile) }

// PluginConfig mcp 插件配置。
type PluginConfig struct {
	Config string `cfg:"config" title:"MCP 配置" description:"显式指定的 MCP 配置 TOML；空 = 用户级 mcp.toml 加受信项目的 mcp.toml"`
}

type plug struct {
	cfg     Config   // 生效的服务（已按启用记录过滤、范围与工作目录已定）
	all     []Server // 全部定义（含停用的），状态用
	ui      assemble.UI
	workdir string
	bound   *resmgr.Bound // 注入的资源管理器视图；nil = Start 时自己建
	local   *resmgr.Manager
	enabled *Enabled
	modes   map[string]toolhub.SandboxMode // stdio 服务生效的沙箱（有沙箱插件时）

	mu     sync.Mutex
	status map[string]*ServerStatus
	tools  []*toolhub.Tool
	leases []*resmgr.Lease
}

func init() { component.Register("mcp", func() component.Component { return &plug{} }) }

func (*plug) Meta() component.Meta {
	return component.Meta{Name: "mcp", Version: "0.2.0", Description: "连接外部 MCP 服务，把它们提供的工具交给模型使用", Effects: []string{component.EffectTools, component.EffectExec, component.EffectNetwork}, Config: &PluginConfig{},
		Requires: []string{assemble.ServiceUI, component.KeyLayers.Maybe(), toolhub.KeySandbox.Maybe(), toolhub.KeySandboxPolicy.Maybe(), "?" + pkgs.Service,
			"?" + assemble.ServiceWorkdir, resmgr.KeyBound.Maybe()},
		Provides: []string{Service}}
}

func (p *plug) Setup(cx *component.Registrar) error {
	pc, _ := cx.Config().(*PluginConfig)
	if pc == nil {
		pc = &PluginConfig{}
	}
	var err error
	if p.ui, err = component.Use[assemble.UI](cx, assemble.ServiceUI); err != nil {
		return err
	}
	if wd, ok, err := component.UseOptional[string](cx, assemble.ServiceWorkdir); err != nil {
		return err
	} else if ok {
		p.workdir = wd
	}
	if b, ok, err := component.GetOptional(cx, resmgr.KeyBound); err != nil {
		return err
	} else if ok {
		p.bound = b
	}
	var warns []string
	if pc.Config != "" {
		// 显式路径（命令行 -config）：调用方点名要读这个文件，路径按原样用，不看来源，也不看启用记录。
		if p.cfg, err = LoadConfig(pc.Config); err != nil {
			return err
		}
		for i := range p.cfg.Server {
			p.cfg.Server[i].Source = SourceConfig
		}
	} else if p.cfg, warns, err = configFromLayers(cx); err != nil {
		return err
	}
	if err := p.addPackages(cx); err != nil {
		return err
	}
	for _, w := range warns {
		p.ui.Warn("MCP", w)
	}
	p.placeServers()
	p.all = append([]Server(nil), p.cfg.Server...)
	if pc.Config == "" {
		if err := p.applyEnabled(); err != nil {
			p.ui.Warn("MCP", err.Error()+"（按全部启用）")
		}
	}
	if err := p.wireConfine(cx); err != nil {
		return err
	}
	p.status = map[string]*ServerStatus{}
	for _, it := range Items(p.cfg) {
		component.RegisterAt(cx, assemble.PermItems, "mcp", it)
	}
	// MCP 的工具要等连接起来（Start）才知道，所以懒取；取到的是同一批只读对象。
	component.RegisterAt(cx, assemble.SessionTools, "mcp", assemble.ToolsEntry{Order: 50,
		Tools: func() []*toolhub.Tool {
			p.mu.Lock()
			defer p.mu.Unlock()
			return p.tools
		}})
	return cx.Provide("mcp", &ServiceAPI{p: p})
}

// placeServers 定下每个服务的范围与工作目录：项目来源一律按工作区；按工作区分配的 stdio 服务
// 工作目录缺省为项目根，相对路径按项目根解析。
func (p *plug) placeServers() {
	for i := range p.cfg.Server {
		s := &p.cfg.Server[i]
		if s.Scope == "" {
			s.Scope = ScopeWorkspace
		}
		if s.Command == "" || s.Scope != ScopeWorkspace || p.workdir == "" {
			continue
		}
		if s.Cwd == "" {
			s.Cwd = p.workdir
		} else if !filepath.IsAbs(s.Cwd) {
			s.Cwd = filepath.Join(p.workdir, s.Cwd)
		}
	}
}

// applyEnabled 按本工作区的启用记录去掉停用的服务（p.all 仍保留全部定义）。
func (p *plug) applyEnabled() error {
	p.enabled = &Enabled{}
	if p.workdir != "" {
		p.enabled.Workspace = WorkspaceKey(p.workdir)
	}
	names := make([]string, len(p.cfg.Server))
	for i, s := range p.cfg.Server {
		names[i] = s.Name
	}
	st, err := p.enabled.Statuses(names)
	if err != nil {
		return err
	}
	var on []Server
	for _, s := range p.cfg.Server {
		if st[s.Name].Enabled {
			on = append(on, s)
		}
	}
	p.cfg.Server = on
	return nil
}

// instance 资源管理器里的一份 MCP 实例：一条连接与它的工具（工具是只读对象，共用这份实例的工作区共用同一批）。
type instance struct {
	client *Client
	remote []RemoteTool
	tools  []*toolhub.Tool
}

// resourceDef 服务 s 在资源管理器里的定义。ID 带配置指纹：定义或生效沙箱变了就是另一份实例，
// 旧实例没人引用后按空闲时长回收。
func (p *plug) resourceDef(s Server) resmgr.Def {
	sv := p.cfg.confined(s)
	return resmgr.Def{Kind: ResourceKind, ID: s.Name + "@" + p.fingerprint(s), Name: s.Name,
		Scope: resmgr.Scope(s.Scope),
		Open: func(ctx context.Context) (any, func(), error) {
			c, err := Connect(ctx, sv)
			if err != nil {
				return nil, nil, err
			}
			rts, err := c.ListTools(ctx)
			if err != nil {
				c.Close()
				return nil, nil, fmt.Errorf("mcp: %s tools/list 失败：%v", s.Name, err)
			}
			return &instance{client: c, remote: rts, tools: c.Tools(rts)}, func() { c.Close() }, nil
		}}
}

// fingerprint 定义的指纹：导出字段（不含来源）加生效的沙箱模式。
func (p *plug) fingerprint(s Server) string {
	s.Source = ""
	b, _ := json.Marshal(struct {
		S        Server
		NoExpand bool
		Sandbox  toolhub.SandboxMode
	}{s, s.noExpand, p.modes[s.Name]})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// confined 按 Config.Confine 给 stdio 服务填上沙箱钩子（Open 与资源定义共用）。
func (c Config) confined(sv Server) Server {
	if c.Confine != nil && sv.Command != "" {
		conf, orig := c.Confine, sv
		sv.confine = func(cmd *exec.Cmd) (func(), error) { return conf(cmd, orig) }
	}
	return sv
}

func (p *plug) Start(sc *component.Scope) error {
	on := map[string]bool{}
	for _, s := range p.cfg.Server {
		on[s.Name] = true
	}
	p.mu.Lock()
	for _, s := range p.all {
		st := p.newStatus(s, StateDisabled)
		st.Enabled = on[s.Name]
		p.status[s.Name] = st
	}
	p.mu.Unlock()
	if len(p.cfg.Server) == 0 {
		return nil
	}
	b := p.bound
	if b == nil { // 没有应用级资源管理器：本运行时自己的，引用归零立即关（与原来「运行时关掉就断开」相同）
		p.local = resmgr.New(resmgr.Options{Idle: -1})
		ws := p.workdir
		if ws == "" {
			ws = "."
		}
		b = p.local.Bind(resmgr.Owner{Workspace: WorkspaceKey(ws)})
	}
	type result struct {
		l   *resmgr.Lease
		err error
	}
	res := make([]result, len(p.cfg.Server))
	var wg sync.WaitGroup
	for i, s := range p.cfg.Server { // 各服务并行连接，结果按配置顺序合并（审查 P2 工具体验）
		wg.Add(1)
		go func(i int, s Server) {
			defer wg.Done()
			l, err := b.Acquire(context.Background(), p.resourceDef(s))
			res[i] = result{l, err}
		}(i, s)
	}
	wg.Wait()
	p.mu.Lock()
	for i, s := range p.cfg.Server {
		st := p.status[s.Name]
		if err := res[i].err; err != nil {
			st.State, st.Error = StateFailed, err.Error()
			p.mu.Unlock()
			p.ui.Warn("MCP", err.Error())
			p.mu.Lock()
			continue
		}
		in := res[i].l.Value().(*instance)
		st.State, st.Instance = StateConnected, res[i].l.Key()
		for _, rt := range in.remote {
			st.Tools = append(st.Tools, ToolInfo{Name: rt.Name, Public: PublicName(s.Name, rt.Name), Description: rt.Description})
		}
		p.tools = append(p.tools, in.tools...)
		p.leases = append(p.leases, res[i].l)
	}
	p.mu.Unlock()
	sc.OnClose(func(context.Context) error { p.release(); return nil })
	return nil
}

// release 交还本运行时取的全部实例引用（全局范围的实例留给别的工作区，没人用后按空闲时长回收）。
func (p *plug) release() {
	p.mu.Lock()
	ls := p.leases
	p.leases = nil
	p.mu.Unlock()
	for _, l := range ls {
		l.Release()
	}
	if p.local != nil {
		p.local.Close()
	}
}

// configFromLayers 从部署期配置来源取 mcp 定义：用户层（全局定义）在前，受信的项目层在后；
// 项目层与用户层重名时项目覆盖并给出提示（warns）。项目层的服务一律按工作区分配。
// 项目层不受信不用。没注入来源返回空配置。
func configFromLayers(cx *component.Registrar) (Config, []string, error) {
	src, ok, err := component.GetOptional(cx, component.KeyLayers)
	if err != nil || !ok {
		return Config{}, nil, err
	}
	layers, err := src.Layers(context.Background())
	if err != nil {
		return Config{}, nil, err
	}
	var out Config
	var warns []string
	at := map[string]int{} // 服务名 → out.Server 下标
	for _, l := range layers {
		var source string
		switch {
		case l.Origin == component.OriginUser:
			source = SourceUser
		case l.Origin == component.OriginProject && l.Trusted:
			source = SourceProject
		default:
			continue
		}
		var c Config
		if err := component.DecodeValue(l.File("mcp"), &c); err != nil {
			return Config{}, nil, fmt.Errorf("mcp: %s: %w", mcpWhere(l), err)
		}
		if err := checkServers(c.Server); err != nil {
			return Config{}, nil, fmt.Errorf("mcp: %s: %w", mcpWhere(l), err)
		}
		for _, s := range c.Server {
			s.Source = source
			if source == SourceProject {
				if s.Scope == ScopeGlobal {
					warns = append(warns, fmt.Sprintf("项目定义的 MCP 服务 %s 写了 scope = \"global\"：项目定义只按工作区分配（%s）", s.Name, mcpWhere(l)))
				}
				s.Scope = ScopeWorkspace
			}
			if i, dup := at[s.Name]; dup {
				warns = append(warns, fmt.Sprintf("项目定义的 MCP 服务 %s 覆盖了用户级的同名服务（%s）", s.Name, mcpWhere(l)))
				out.Server[i] = s
				continue
			}
			at[s.Name] = len(out.Server)
			out.Server = append(out.Server, s)
		}
	}
	return out, warns, nil
}

func mcpWhere(l component.ConfigLayer) string {
	if l.Where != "" {
		return l.Where
	}
	if l.Name != "" {
		return l.Name
	}
	return l.Origin
}

// defaultSandbox 权限插件提供的“缺省档位下某工具的沙箱”（不 import 权限插件）。
type defaultSandbox interface {
	DefaultSandboxFor(tool string) toolhub.SandboxMode
}

// wireConfine stdio 服务子进程放进沙箱：服务 sandbox 字段优先，否则取缺省档位 [sandbox] 里 mcp__<服务>__* 的值；
// 没有沙箱插件时不加沙箱并告警。
//
// 沙箱按启动它的工作区授权：全局范围的 stdio 服务在第一个用到它的工作区里起进程，之后别的工作区共用这个进程，
// 它的 workspace-write 仍只放行第一个工作区。需要跨工作区写文件的全局服务要显式写 sandbox。
func (p *plug) wireConfine(cx *component.Registrar) error {
	sb, ok, err := component.GetOptional(cx, toolhub.KeySandbox)
	if err != nil {
		return err
	}
	var pol defaultSandbox
	if v, has, err := component.GetOptional(cx, toolhub.KeySandboxPolicy); err != nil {
		return err
	} else if has {
		pol, _ = v.(defaultSandbox)
	}
	if !ok {
		for _, s := range p.cfg.Server {
			if s.Command != "" {
				p.ui.Warn("MCP", fmt.Sprintf("沙箱插件未启用，MCP 服务 %s 的子进程不受沙箱约束", s.Name))
			}
		}
		return nil
	}
	p.modes = map[string]toolhub.SandboxMode{}
	for _, s := range p.cfg.Server {
		if s.Command == "" {
			continue
		}
		mode := toolhub.SandboxWorkspaceWrite
		if s.Sandbox != "" {
			mode = toolhub.SandboxMode(s.Sandbox)
		} else if pol != nil {
			mode = pol.DefaultSandboxFor(PublicName(s.Name, "x"))
		}
		p.modes[s.Name] = mode
	}
	modes := p.modes
	p.cfg.Confine = func(cmd *exec.Cmd, s Server) (func(), error) {
		mode, ok := modes[s.Name]
		if !ok {
			mode = toolhub.SandboxWorkspaceWrite
		}
		return sb.Apply(cmd, mode)
	}
	return nil
}

// addPackages 追加插件包里的 MCP 服务（已由 runtime/pkgs 校验）。${TENON_PLUGIN_DIR} 换成包目录，
// 同名环境变量传给子进程，cwd 缺省为包目录。与配置文件里的服务重名时报错。
// 项目级包的服务一律按工作区分配；用户级包可写 scope。
func (p *plug) addPackages(cx *component.Registrar) error {
	ps, err := pkgs.From(cx)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, s := range p.cfg.Server {
		seen[s.Name] = true
	}
	for _, k := range ps {
		for _, s := range packageServers(k) {
			if seen[s.Name] {
				return fmt.Errorf("mcp: 插件包 %s 的服务名 %q 与已有服务重复", k.File, s.Name)
			}
			seen[s.Name] = true
			env := map[string]string{pkgs.DirVar: k.Dir}
			for n, v := range s.Env {
				env[n] = pkgs.Expand(v, k.Dir)
			}
			args := make([]string, len(s.Args))
			for i, a := range s.Args {
				args[i] = pkgs.Expand(a, k.Dir)
			}
			cwd := pkgs.Expand(s.Cwd, k.Dir)
			if s.Command != "" {
				if cwd == "" {
					cwd = k.Dir
				} else if !filepath.IsAbs(cwd) {
					cwd = filepath.Join(k.Dir, cwd)
				}
			}
			scope := s.Scope
			if k.Scope != SourceUser {
				scope = ScopeWorkspace
			}
			p.cfg.Server = append(p.cfg.Server, Server{Name: s.Name, Command: pkgs.Expand(s.Command, k.Dir), Args: args,
				Env: env, Cwd: cwd, URL: s.URL, Headers: s.Headers, Timeout: s.Timeout, Trust: s.Trust, Sandbox: s.Sandbox,
				Scope: scope, Source: SourcePackage + k.Name, noExpand: true})
		}
	}
	return nil
}
