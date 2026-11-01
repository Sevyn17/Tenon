package sandbox

// 插件登记（thin-kernel-plan §2.2）：提供 service:sandbox（toolhub.Confiner），
// 登记“申请更宽沙箱须确认”的 Guard 层。会话沙箱档位来自 service:permission（toolhub.SandboxPolicy）。

import (
	"ybase-kernel/core/plugin"
	kr "ybase-kernel/core/runtime"
	"ybase-kernel/core/toolhub"
)

// Service 本插件提供的服务键。
const Service = "service:sandbox"

// PolicyService 会话沙箱档位来源（由权限插件提供）。
const PolicyService = "service:permission"

// Config sandbox 插件配置。
type Config struct {
	TempDir     string `cfg:"temp_dir" title:"沙箱临时目录" description:"沙箱专用 TEMP / 构建缓存的上级目录；空 = %LOCALAPPDATA%\\ybase\\sbtmp"`
	KeepGoCache bool   `cfg:"keep_gocache" title:"保留 GOCACHE" description:"不把 GOCACHE 改到沙箱临时目录"`
}

type plug struct{}

func init() { plugin.Register("sandbox", func() plugin.Plugin { return &plug{} }) }

func (*plug) Meta() plugin.Meta {
	return plugin.Meta{Name: "sandbox", Version: "0.1.0", Default: false, // 独立开关，缺省关闭（§9.9）
		Config:   &Config{},
		Requires: []string{kr.ServiceWorkdir, PolicyService}, Provides: []string{Service}}
}

func (*plug) Setup(cx *plugin.SetupCx) error {
	root, err := plugin.Use[string](cx, kr.ServiceWorkdir)
	if err != nil {
		return err
	}
	pol, err := plugin.Use[toolhub.SandboxPolicy](cx, PolicyService)
	if err != nil {
		return err
	}
	ap, _ := pol.(toolhub.SandboxApprover)
	c, _ := cx.Config().(*Config)
	if c == nil {
		c = &Config{}
	}
	cx.Register(kr.PointSessionLayer, "sandbox", kr.LayerEntry{Guard: true, Order: 5,
		Func: func(*kr.SessionCx) ([]toolhub.Layer, error) { return []toolhub.Layer{Layer(pol.SandboxMode, ap)}, nil }})
	return cx.Provide("sandbox", toolhub.Confiner(&Sandbox{Root: root, TempDir: c.TempDir, KeepGoCache: c.KeepGoCache}))
}

func (*plug) Start(*plugin.Scope) error { return nil }
