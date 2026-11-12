package mcp

// 插件登记（thin-kernel-plan §2.2）：MCP 客户端。进程启动时连接一次，工具列表此后不变（T3）。
// mcp.toml 不自己拼路径去读，从 component.KeyLayers 按层取（18 方案第四节）。
// 只有项目层：连哪个服务、什么信任级别跟具体项目绑定，不加用户级（18 方案第七节）。
// 项目层不受信不用。命令行 -config 给的显式路径仍直接读文件。插件包的 [[server]] 走 pkgs.Service。

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"

	"github.com/Sevyn17/tenon/runtime/assemble"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/pkgs"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// PluginConfig mcp 插件配置。
type PluginConfig struct {
	Config string `cfg:"config" title:"MCP 配置" description:"MCP 配置 TOML（缺省 <dir>/.tenon/mcp.toml）"`
}

type plug struct {
	cfg Config
	ui  assemble.UI
	src *Source
}

func init() { component.Register("mcp", func() component.Component { return &plug{} }) }

func (*plug) Meta() component.Meta {
	return component.Meta{Name: "mcp", Version: "0.1.0", Effects: []string{component.EffectTools, component.EffectExec, component.EffectNetwork}, Config: &PluginConfig{},
		Requires: []string{assemble.ServiceUI, component.KeyLayers.Maybe(), toolhub.KeySandbox.Maybe(), toolhub.KeySandboxPolicy.Maybe(), "?" + pkgs.Service}}
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
	if pc.Config != "" {
		// 显式路径（命令行 -config）：调用方点名要读这个文件，路径按原样用，不看来源。
		if p.cfg, err = LoadConfig(pc.Config); err != nil {
			return err
		}
	} else if p.cfg, err = configFromLayers(cx); err != nil {
		return err
	}
	if err := p.addPackages(cx); err != nil {
		return err
	}
	if err := p.wireConfine(cx); err != nil {
		return err
	}
	for _, it := range Items(p.cfg) {
		component.RegisterAt(cx, assemble.PermItems, "mcp", it)
	}
	// MCP 的工具要等进程起来（Start）才知道，所以懒取；取到的是同一批只读对象。
	component.RegisterAt(cx, assemble.SessionTools, "mcp", assemble.ToolsEntry{Order: 50,
		Tools: func() []*toolhub.Tool {
			if p.src == nil {
				return nil
			}
			return p.src.Tools
		}})
	return nil
}

func (p *plug) Start(sc *component.Scope) error {
	if len(p.cfg.Server) == 0 {
		return nil
	}
	p.src = Open(context.Background(), p.cfg)
	for _, w := range p.src.Warnings {
		p.ui.Warn("MCP", w)
	}
	sc.OnClose(func(context.Context) error { p.src.Close(); return nil })
	return nil
}

// configFromLayers 从部署期配置来源取 mcp 配置。
// 只认项目层：服务的信任级别跟具体项目绑定，做成用户级会悄悄作用于所有项目（18 方案第七节）。
// 项目层不受信不用。没注入来源返回空配置。
func configFromLayers(cx *component.Registrar) (Config, error) {
	src, ok, err := component.GetOptional(cx, component.KeyLayers)
	if err != nil || !ok {
		return Config{}, err
	}
	layers, err := src.Layers(context.Background())
	if err != nil {
		return Config{}, err
	}
	var out Config
	for _, l := range layers {
		if l.Origin != component.OriginProject || !l.Trusted {
			continue
		}
		var c Config
		if err := component.DecodeValue(l.File("mcp"), &c); err != nil {
			return Config{}, fmt.Errorf("mcp: %s: %w", mcpWhere(l), err)
		}
		if err := checkServers(c.Server); err != nil {
			return Config{}, fmt.Errorf("mcp: %s: %w", mcpWhere(l), err)
		}
		out.Server = append(out.Server, c.Server...)
	}
	return out, nil
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
	p.cfg.Confine = func(cmd *exec.Cmd, s Server) (func(), error) {
		mode := toolhub.SandboxWorkspaceWrite
		if s.Sandbox != "" {
			mode = toolhub.SandboxMode(s.Sandbox)
		} else if pol != nil {
			mode = pol.DefaultSandboxFor(PublicName(s.Name, "x"))
		}
		return sb.Apply(cmd, mode)
	}
	return nil
}

// addPackages 追加插件包里的 MCP 服务（已由 runtime/pkgs 校验）。${TENON_PLUGIN_DIR} 换成包目录，
// 同名环境变量传给子进程，cwd 缺省为包目录。与配置文件里的服务重名时报错。
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
			p.cfg.Server = append(p.cfg.Server, Server{Name: s.Name, Command: pkgs.Expand(s.Command, k.Dir), Args: args,
				Env: env, Cwd: cwd, URL: s.URL, Headers: s.Headers, Timeout: s.Timeout, Trust: s.Trust, Sandbox: s.Sandbox,
				noExpand: true})
		}
	}
	return nil
}
