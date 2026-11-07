package shell

// 插件登记（thin-kernel-plan §2.2）：shell / job_output / job_kill。
// 沙箱（service:sandbox）与会话沙箱档位（service:permission）都是可选依赖；缺沙箱时命令不受限。

import (
	"context"

	"tenon/runtime/assemble"
	"tenon/runtime/component"
	"tenon/runtime/toolhub"
)

// Config shell 插件配置（缺省全开，与改版前行为一致）。
type Config struct {
	StripANSI bool `cfg:"strip_ansi" default:"true" description:"去掉输出里的终端颜色 / 控制序列"`
	DenyHint  bool `cfg:"deny_hint" default:"true" description:"沙箱拒绝时在结果末尾提示可申请更宽沙箱"`
	Intro     bool `cfg:"intro" default:"true" description:"工具描述里说明本平台 shell 语法"`
}

type plug struct{ sh *Shell }

func init() { component.Register("shell", func() component.Component { return &plug{} }) }

func (*plug) Meta() component.Meta {
	return component.Meta{Name: "shell", Version: "0.1.0", Default: true, Effects: []string{component.EffectTools, component.EffectExec},
		Config:   &Config{},
		Requires: []string{assemble.ServiceWorkdir, toolhub.KeySandbox.Maybe(), toolhub.KeySandboxPolicy.Maybe()}}
}

func (p *plug) Setup(cx *component.Registrar) error {
	root, err := component.Use[string](cx, assemble.ServiceWorkdir)
	if err != nil {
		return err
	}
	sh := NewShell(root) // 后台 job 属于本进程，退出时全部终止
	if c, _ := cx.Config().(*Config); c != nil {
		sh.KeepANSI, sh.NoDenyHint, sh.NoIntro = !c.StripANSI, !c.DenyHint, !c.Intro
	}
	sb, ok, err := component.GetOptional(cx, toolhub.KeySandbox)
	if err != nil {
		return err
	}
	if ok {
		sh.Sandbox = sb
		sh.Mode = toolhub.SandboxWorkspaceWrite
	}
	pol, ok, err := component.GetOptional(cx, toolhub.KeySandboxPolicy)
	if err != nil {
		return err
	}
	if ok {
		sh.ModeFor = pol.SandboxMode // 档位切换即时生效
		sh.Approver, _ = pol.(toolhub.SandboxApprover)
	}
	p.sh = sh
	for _, it := range Items() {
		cx.Register(assemble.PointPermItems, it.ID, it)
	}
	// 进程级一个 Shell（后台 job 属于本进程），工具装配期构造一次。
	tools := sh.Tools()
	cx.Register(assemble.PointSessionTools, "shell", assemble.ToolsEntry{Order: 30,
		Tools: func() []*toolhub.Tool { return tools }})
	return nil
}

func (p *plug) Start(sc *component.Scope) error {
	sc.OnClose(func(context.Context) error { p.sh.Close(); return nil })
	return nil
}
