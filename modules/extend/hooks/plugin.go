package hooks

// 插件登记（thin-kernel-plan §2.2）：用户脚本钩子。用户级 ~/.tenon/hooks.toml 总是加载；
// 项目级 <dir>/.tenon/hooks.toml 会执行仓库里写的命令，需配置 project_hooks（命令行 -project-hooks）显式开启。
// 前置钩子是 Guard（排在权限之后：被拒的调用不触发钩子），后置钩子是 Transform（只处理执行成功的结果）。

import (
	"context"
	"path/filepath"

	"github.com/Sevyn17/tenon/runtime/appid"
	"github.com/Sevyn17/tenon/runtime/assemble"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/pkgs"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// PluginConfig hooks 插件配置。
type PluginConfig struct {
	ProjectHooks bool `cfg:"project_hooks" title:"加载项目级钩子" description:"加载 <dir>/.tenon/hooks.toml（会执行仓库里写的命令）"`
}

type plug struct{ r *Runner }

func init() { component.Register("hooks", func() component.Component { return &plug{} }) }

func (*plug) Meta() component.Meta {
	return component.Meta{Name: "hooks", Version: "0.1.0", Effects: []string{component.EffectExec, component.EffectRewriteResult}, Config: &PluginConfig{},
		Requires: []string{assemble.ServiceWorkdir, assemble.ServiceUI, "?" + assemble.ServiceTrust, toolhub.KeySandbox.Maybe(), "?" + pkgs.Service}}
}

func (p *plug) Setup(cx *component.Registrar) error {
	cfg, _ := cx.Config().(*PluginConfig)
	if cfg == nil {
		cfg = &PluginConfig{}
	}
	root, err := component.Use[string](cx, assemble.ServiceWorkdir)
	if err != nil {
		return err
	}
	ui, err := component.Use[assemble.UI](cx, assemble.ServiceUI)
	if err != nil {
		return err
	}
	var cs []Config
	if home := userHome(); home != "" {
		c, err := LoadConfig(filepath.Join(home, "hooks.toml"))
		if err != nil {
			return err
		}
		cs = append(cs, c)
	}
	// 项目级钩子：显式 project_hooks，或应用明确预置“项目受信”（未预置信任服务时不自动加载）。
	trusted, known, _ := component.UseOptional[bool](cx, assemble.ServiceTrust)
	if cfg.ProjectHooks || (known && trusted) {
		c, err := LoadConfig(filepath.Join(appid.Project(root), "hooks.toml"))
		if err != nil {
			return err
		}
		for i := range c.Hook {
			c.Hook[i].scrub = true // 项目目录来源：不给宿主密钥
		}
		cs = append(cs, c)
	}
	// 插件包里的钩子（已由 runtime/pkgs 校验）：排在用户级、项目级之后。
	ps, err := pkgs.From(cx)
	if err != nil {
		return err
	}
	for _, k := range ps {
		var c Config
		for _, h := range packageHooks(k) { // 段已由本组件解码校验，matcher 已编译
			h.Command, h.scrub = pkgs.Expand(h.Command, k.Dir), true
			c.Hook = append(c.Hook, h)
		}
		cs = append(cs, c)
	}
	r := New(Merge(cs...), root, func(s string) { ui.Warn("钩子", s) })
	p.r = r
	// 钩子命令放进沙箱（缺省 workspace-write，按条可改）；无沙箱插件时照常执行。
	if sb, ok, err := component.GetOptional(cx, toolhub.KeySandbox); err != nil {
		return err
	} else if ok {
		r.Confine = sb.Apply
	}
	split := func(guard bool) []toolhub.Layer {
		var ls []toolhub.Layer
		for _, l := range r.Layers() {
			if l.Guard == guard {
				ls = append(ls, l)
			}
		}
		return ls
	}
	component.RegisterAt(cx, assemble.SessionLayer, "hooks-post", assemble.LayerEntry{Order: 10,
		Func: func(*assemble.SessionCx) ([]toolhub.Layer, error) { return split(false), nil }})
	component.RegisterAt(cx, assemble.SessionLayer, "hooks-pre", assemble.LayerEntry{Guard: true, Order: 20,
		Func: func(*assemble.SessionCx) ([]toolhub.Layer, error) { return split(true), nil }})
	component.RegisterAt(cx, assemble.AfterTurn, "hooks", assemble.AfterTurnFunc(r.AfterTurn))
	return nil
}

// Start 关闭时等异步的 stop 钩子跑完（审查 P1-30）。
func (p *plug) Start(sc *component.Scope) error {
	sc.OnClose(func(context.Context) error {
		if p.r != nil {
			p.r.Wait()
		}
		return nil
	})
	return nil
}

// userHome 用户全局目录：应用的用户级目录（appid.Home）；取不到返回空（与权限、技能、指令组件同一约定）。
func userHome() string { return appid.Home() }
