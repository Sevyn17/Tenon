package sandbox

// 插件登记（thin-kernel-plan §2.2）：提供 service:sandbox（toolhub.Confiner），
// 登记“申请更宽沙箱须确认”的 Guard 层。会话沙箱档位来自 service:permission（toolhub.SandboxPolicy）。

import (
	"ybase/runtime/assemble"
	"ybase/runtime/component"
	"ybase/runtime/toolhub"
)

// Service 本插件提供的服务键。
const Service = "service:sandbox"

// PolicyService 会话沙箱档位来源（由权限插件提供）。
const PolicyService = "service:permission"

// Config sandbox 插件配置。
type Config struct {
	TempDir     string `cfg:"temp_dir" title:"沙箱临时目录" description:"沙箱专用 TEMP / 构建缓存的上级目录；空 = %LOCALAPPDATA%\\ybase\\sbtmp"`
	NoPrepare   bool   `cfg:"no_prepare" title:"不预先授权" description:"启动时不在后台为工作区做沙箱授权（改到第一次受限执行时再做）"`
	KeepGoCache bool   `cfg:"keep_gocache" title:"保留 GOCACHE" description:"不把 GOCACHE 改到沙箱临时目录"`
}

type plug struct {
	sb        *Sandbox
	noPrepare bool
	ui        assemble.UI
}

func init() { component.Register("sandbox", func() component.Component { return &plug{} }) }

func (*plug) Meta() component.Meta {
	return component.Meta{Name: "sandbox", Version: "0.1.0", Default: false, // 独立开关，缺省关闭（§9.9）
		Config:   &Config{},
		Requires: []string{assemble.ServiceWorkdir, PolicyService, "?" + assemble.ServiceUI}, Provides: []string{Service}}
}

func (p *plug) Setup(cx *component.Registrar) error {
	root, err := component.Use[string](cx, assemble.ServiceWorkdir)
	if err != nil {
		return err
	}
	pol, err := component.Use[toolhub.SandboxPolicy](cx, PolicyService)
	if err != nil {
		return err
	}
	ap, _ := pol.(toolhub.SandboxApprover)
	c, _ := cx.Config().(*Config)
	if c == nil {
		c = &Config{}
	}
	cx.Register(assemble.PointSessionLayer, "sandbox", assemble.LayerEntry{Guard: true, Order: 5,
		Func: func(*assemble.SessionCx) ([]toolhub.Layer, error) {
			return []toolhub.Layer{Layer(pol.SandboxMode, ap)}, nil
		}})
	if !platformSupported {
		if ui, ok, _ := component.UseOptional[assemble.UI](cx, assemble.ServiceUI); ok && ui != nil {
			ui.Warn("沙箱", "本平台尚无沙箱实现：shell、用户钩子、MCP 子进程按 danger-full-access 执行，不受限")
		}
	}
	p.sb = &Sandbox{Root: root, TempDir: c.TempDir, KeepGoCache: c.KeepGoCache, Degrade: true}
	p.noPrepare = c.NoPrepare
	p.ui, _, _ = component.UseOptional[assemble.UI](cx, assemble.ServiceUI)
	return cx.Provide("sandbox", toolhub.Confiner(p.sb))
}

// Start 在后台预先为工作区授权（审查 P1-45）：失败只告警，第一次受限执行时会再试一次并报错。
func (p *plug) Start(*component.Scope) error {
	if p.sb == nil || p.noPrepare || !platformSupported {
		return nil
	}
	go func() {
		if err := p.sb.Prepare(); err != nil && p.ui != nil {
			p.ui.Warn("沙箱", "工作区预授权失败（第一次受限执行时会再试）："+err.Error())
		}
	}()
	return nil
}
