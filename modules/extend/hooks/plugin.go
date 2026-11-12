package hooks

// 插件登记（thin-kernel-plan §2.2）：用户脚本钩子。
// 配置不自己读文件：从部署期配置来源（component.KeyLayers）按层取（18 方案第四节）。
// 用户层总是加载；项目层会执行仓库里写的命令，需 project_hooks（命令行 -project-hooks）
// 显式开启，或项目层标了受信。插件包里的钩子走 pkgs.Service，排在两层之后。
// 前置钩子是 Guard（排在权限之后：被拒的调用不触发钩子），后置钩子是 Transform（只处理执行成功的结果）。

import (
	"context"

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
		Requires: []string{assemble.ServiceWorkdir, assemble.ServiceUI, component.KeyLayers.Maybe(), toolhub.KeySandbox.Maybe(), "?" + pkgs.Service}}
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
	src, ok, err := component.GetOptional(cx, component.KeyLayers)
	if err != nil {
		return err
	}
	if ok {
		layers, err := src.Layers(context.Background())
		if err != nil {
			return err
		}
		for _, l := range layers {
			// 项目层是执行闸门：显式 project_hooks，或该层标了受信，才加载。
			if l.Origin == component.OriginProject && !cfg.ProjectHooks && !l.Trusted {
				continue
			}
			var c Config
			if err := component.DecodeValue(l.File("hooks"), &c); err != nil {
				return err
			}
			if err := checkHooks(c.Hook); err != nil {
				return err
			}
			// 脱敏跟来源走，不跟层的位置走：用户来源给宿主环境，其余来源不给密钥。
			if l.Origin != component.OriginUser {
				for i := range c.Hook {
					c.Hook[i].scrub = true
				}
			}
			cs = append(cs, c)
		}
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
	// 04 4.3.1：不再登记「会话层」，改成 ToolCall 改写区的中间件（pre 阻止 / post 追加反馈）。
	for _, mw := range r.Middlewares() {
		component.Wrap(cx, component.ToolCall, mw)
	}
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
