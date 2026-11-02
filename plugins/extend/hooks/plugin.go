package hooks

// 插件登记（thin-kernel-plan §2.2）：用户脚本钩子。用户级 ~/.ybase/hooks.toml 总是加载；
// 项目级 <dir>/.ybase/hooks.toml 会执行仓库里写的命令，需配置 project_hooks（命令行 -project-hooks）显式开启。
// 前置钩子是 Guard（排在权限之后：被拒的调用不触发钩子），后置钩子是 Transform（只处理执行成功的结果）。

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"ybase-kernel/core/pkgs"
	"ybase-kernel/core/plugin"
	kr "ybase-kernel/core/runtime"
	"ybase-kernel/core/toolhub"
)

// PluginConfig hooks 插件配置。
type PluginConfig struct {
	ProjectHooks bool `cfg:"project_hooks" title:"加载项目级钩子" description:"加载 <dir>/.ybase/hooks.toml（会执行仓库里写的命令）"`
}

type plug struct{}

func init() { plugin.Register("hooks", func() plugin.Plugin { return &plug{} }) }

func (*plug) Meta() plugin.Meta {
	return plugin.Meta{Name: "hooks", Version: "0.1.0", Effects: []string{plugin.EffectExec, plugin.EffectRewriteResult}, Config: &PluginConfig{},
		Requires: []string{kr.ServiceWorkdir, kr.ServiceUI, "?" + kr.ServiceTrust, "?service:sandbox", "?" + pkgs.Service}}
}

func (*plug) Setup(cx *plugin.Registrar) error {
	cfg, _ := cx.Config().(*PluginConfig)
	if cfg == nil {
		cfg = &PluginConfig{}
	}
	root, err := plugin.Use[string](cx, kr.ServiceWorkdir)
	if err != nil {
		return err
	}
	ui, err := plugin.Use[kr.UI](cx, kr.ServiceUI)
	if err != nil {
		return err
	}
	var cs []Config
	if home, err := os.UserHomeDir(); err == nil {
		c, err := LoadConfig(filepath.Join(home, ".ybase", "hooks.toml"))
		if err != nil {
			return err
		}
		cs = append(cs, c)
	}
	// 项目级钩子：显式 project_hooks，或应用明确预置“项目受信”（未预置信任服务时不自动加载）。
	trusted, known, _ := plugin.UseOptional[bool](cx, kr.ServiceTrust)
	if cfg.ProjectHooks || (known && trusted) {
		c, err := LoadConfig(filepath.Join(root, ".ybase", "hooks.toml"))
		if err != nil {
			return err
		}
		cs = append(cs, c)
	}
	// 插件包里的钩子（已由 core/pkgs 校验）：排在用户级、项目级之后。
	ps, err := pkgs.From(cx)
	if err != nil {
		return err
	}
	for _, k := range ps {
		var c Config
		for _, h := range k.Hook {
			hk := Hook{Event: h.Event, Matcher: h.Matcher, Command: pkgs.Expand(h.Command, k.Dir), Timeout: h.Timeout, Sandbox: h.Sandbox}
			if h.Matcher != "" {
				re, err := regexp.Compile("^(?:" + h.Matcher + ")$")
				if err != nil {
					return fmt.Errorf("hooks: 插件包 %s：matcher：%w", k.File, err)
				}
				hk.re = re
			}
			c.Hook = append(c.Hook, hk)
		}
		cs = append(cs, c)
	}
	r := New(Merge(cs...), root, func(s string) { ui.Warn("钩子", s) })
	// 钩子命令放进沙箱（缺省 workspace-write，按条可改）；无沙箱插件时照常执行。
	if sb, ok, err := plugin.UseOptional[toolhub.Confiner](cx, "service:sandbox"); err != nil {
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
	cx.Register(kr.PointSessionLayer, "hooks-post", kr.LayerEntry{Order: 10,
		Func: func(*kr.SessionCx) ([]toolhub.Layer, error) { return split(false), nil }})
	cx.Register(kr.PointSessionLayer, "hooks-pre", kr.LayerEntry{Guard: true, Order: 20,
		Func: func(*kr.SessionCx) ([]toolhub.Layer, error) { return split(true), nil }})
	cx.Register(kr.PointAfterTurn, "hooks", kr.AfterTurnFunc(r.AfterTurn))
	return nil
}

func (*plug) Start(*plugin.Scope) error { return nil }
