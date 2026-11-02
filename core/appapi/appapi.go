// Package appapi 应用接口层（docs/90 - 归档/10 - 框架模块/12 - 应用接口层.md）。
//
// CLI、桌面端（Wails）、api-server 都只调这一层：命令是 App 的方法（进程内直接调），
// 也可经 Call(cmd, args JSON) 统一分发；事件经 Events（易失）与 Follow（可靠，回放 + 实时）取得。
//
// 本包只依赖 core 包。权限档位、压缩等插件能力经小接口（Modes、Compactor）注入，
// 由应用用插件服务对象实现；appapi 不 import 任何插件包（runtime/deps_test.go）。
package appapi

import (
	"context"
	"errors"
	"sync"

	"ybase-kernel/core/agent"
	"ybase-kernel/core/bus"
	"ybase-kernel/core/host"
	"ybase-kernel/core/pkgs"
	"ybase-kernel/core/plugin"
	"ybase-kernel/core/runtime"
	"ybase-kernel/core/session"
	"ybase-kernel/core/store"
)

// Protocol 协议版本：同版本只加字段和命令；改语义或删字段升版本（§六）。
const Protocol = 1

// Server hello 返回的服务端名。
const Server = "ybase-kernel"

// Options 应用装配好的部件。Host、Bus、DB 必填，其余可空。
type Options struct {
	Host *host.Host
	Bus  *bus.Bus
	DB   store.Store
	// Builder 会话构造器：非空时 New 用它设置 Host.New / Host.Child，并在其钩子上并入流式事件（StreamHooks）。
	// 为空时 Host.New / Host.Child 须由应用自己设置。
	Builder *runtime.Builder

	Model   string // 缺省模型名（hello 显示）
	Workdir string
	Trusted bool // 项目是否受信

	Modes     Modes           // 权限档位（permission 插件适配）；nil = 未装
	Compactor Compactor       // 手动压缩（compact-basic 适配）；nil = 未装
	Profile   *plugin.Profile // plugins.list 用；nil = 不提供
	Packages  []*pkgs.Package // packages.list 用：已加载的插件包
	UI        *UI             // ask 的回答经 session.answer 送回；nil = 应用自己实现 runtime.UI
}

// App 一个进程一个：持有装配结果，对外提供命令与事件。方法并发安全。
type App struct {
	o       Options
	streams sync.Map // 会话 id → *streamer
	pend    pendingSet
}

// New 构造 App：把 DB 的提交事件接到总线（Follow 的实时部分）；包一层 Host.Hooks
// （回合开始发档位变化提示 notice，回合结束先发完积压正文再交给原钩子）；给了 Builder 时设置会话构造器。
// Host.Hooks 里应用已设的 BeforeTurn / AfterTurn 保留并在包装里调用，须在 New 之前设置。
func New(o Options) (*App, error) {
	if o.Host == nil || o.Bus == nil || o.DB == nil {
		return nil, errors.New("appapi: Host / Bus / DB 必填")
	}
	if o.Host.DB == nil {
		o.Host.DB = o.DB
	}
	if o.Host.Bus == nil {
		o.Host.Bus = o.Bus
	}
	a := &App{o: o}
	o.DB.OnCommit(o.Bus.Publish)
	a.watchPending(context.Background()) // 总线关闭时退出

	before, after := o.Host.Hooks.BeforeTurn, o.Host.Hooks.AfterTurn
	o.Host.Hooks.BeforeTurn = func(id string, in session.Content, untrusted bool) (session.Content, host.Decision, error) {
		if o.Modes != nil { // 自定义档位文件有变化时重读并提示
			if n := o.Modes.Notice(id); n != "" {
				a.Notice(id, n)
			}
		}
		if before != nil {
			return before(id, in, untrusted)
		}
		return in, host.Allow, nil
	}
	o.Host.Hooks.AfterTurn = func(id string, end session.EndReason) {
		a.flush(id)
		if after != nil {
			after(id, end)
		}
	}

	if rb := o.Builder; rb != nil {
		orig := rb.Hooks
		rb.Hooks = func(cx *runtime.SessionCx) agent.Hooks {
			hk := a.StreamHooks(cx.ID)
			if orig != nil {
				hk = MergeHooks(orig(cx), hk)
			}
			return hk
		}
		o.Host.New = func(id, def string) (*agent.Agent, *session.Config, error) { return rb.Build(id, def, false) }
		o.Host.Child = func(id, def string) (*agent.Agent, *session.Config, error) { return rb.Build(id, def, true) }
	}
	return a, nil
}

// Notice 发提示事件 notice{text}（档位变化、异步持久化失败等）。
func (a *App) Notice(sessionID, text string) {
	a.o.Bus.Publish(bus.Envelope{Session: sessionID, Kind: EventNotice, Data: map[string]any{"text": text}})
}

// Events 易失订阅（流式正文、回合状态、确认请求、提示等，也含持久事件的实时副本）。ctx 结束自动注销。
func (a *App) Events(ctx context.Context, f bus.Filter) *bus.Sub { return a.o.Bus.Subscribe(ctx, f) }

// Follow 可靠订阅会话的持久事件 entry / rewind：回放 seq > after → synced → 实时，不漏不重。
func (a *App) Follow(ctx context.Context, sessionID string, after int64) (*bus.Sub, error) {
	if sessionID == "" {
		return nil, Errorf(CodeBadRequest, "Follow 必须指定会话")
	}
	s, err := a.o.Bus.Follow(ctx, a.o.DB, bus.Filter{Session: sessionID}, after)
	return s, wrap(err)
}

// HasCompactor 是否装了手动压缩。
func (a *App) HasCompactor() bool { return a.o.Compactor != nil }

// Host 底层会话宿主（应用做 CLI 专属的事时用，如装配快照）。
func (a *App) Host() *host.Host { return a.o.Host }

// DB 底层存储。
func (a *App) DB() store.Store { return a.o.DB }

// Bus 事件总线。
func (a *App) Bus() *bus.Bus { return a.o.Bus }

// Empty 无返回值命令的结果（JSON 为 {}）。
type Empty struct{}

// NoArgs 无参数命令的参数。
type NoArgs struct{}

// HelloArgs 握手参数。Version 为客户端支持的协议版本，0 = 不声明。
type HelloArgs struct {
	Client  string `json:"client"`
	Version int    `json:"version"`
}

// HelloResult 握手结果。
type HelloResult struct {
	Protocol int    `json:"protocol"`
	Server   string `json:"server"`
	Model    string `json:"model"`
	Workdir  string `json:"workdir"`
	Trusted  bool   `json:"trusted"`
}

// Hello 握手：协商协议版本；版本不支持返回 unsupported。
func (a *App) Hello(_ context.Context, in HelloArgs) (HelloResult, error) {
	if in.Version != 0 && in.Version != Protocol {
		return HelloResult{}, Errorf(CodeUnsupported, "协议版本 %d 不支持（服务端 %d）", in.Version, Protocol)
	}
	return HelloResult{Protocol: Protocol, Server: Server, Model: a.o.Model, Workdir: a.o.Workdir, Trusted: a.o.Trusted}, nil
}

// PluginInfo plugins.list 的一行。
type PluginInfo struct {
	Name    string         `json:"name"`
	On      bool           `json:"on"`
	Default bool           `json:"default"`
	Source  string         `json:"source"`
	Effects []string       `json:"effects"`
	Schema  []plugin.Field `json:"schema"`
}

// PluginsList 全部已登记插件的开关状态与配置 Schema（改开关需重启，见 PluginsSet）。
func (a *App) PluginsList(_ context.Context, _ NoArgs) ([]PluginInfo, error) {
	if a.o.Profile == nil {
		return nil, Errorf(CodeUnsupported, "未提供插件信息")
	}
	var out []PluginInfo
	for _, st := range a.o.Profile.States() {
		pi := PluginInfo{Name: st.Name, On: st.On, Default: st.Default, Source: st.Source, Effects: st.Effects}
		if m, ok := plugin.MetaOf(st.Name); ok {
			pi.Schema = plugin.SchemaOf(m.Config)
		}
		out = append(out, pi)
	}
	return out, nil
}

// PackageInfo packages.list 的一行（插件包，见 docs/90 - 归档/10 - 框架模块/15 - 插件包.md）。
type PackageInfo struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	Scope       string `json:"scope"`  // user / project
	Source      string `json:"source"` // 清单路径
	Contents    string `json:"contents"`
}

// PackagesList 已加载的插件包（用户级在前、项目级在后）。
func (a *App) PackagesList(_ context.Context, _ NoArgs) ([]PackageInfo, error) {
	out := []PackageInfo{}
	for _, k := range a.o.Packages {
		out = append(out, PackageInfo{Name: k.Name, Version: k.Version, Description: k.Description,
			Scope: k.Scope, Source: k.File, Contents: k.Contents()})
	}
	return out, nil
}

// PluginsSetArgs plugins.set 参数。
type PluginsSetArgs struct {
	Name    string         `json:"name"`
	Enabled *bool          `json:"enabled,omitempty"`
	Config  map[string]any `json:"config,omitempty"`
	Scope   string         `json:"scope"` // user / project
}

// PluginsSet 改插件开关 / 配置：未实现（需写 profile 文件，留到桌面端）。
func (a *App) PluginsSet(_ context.Context, _ PluginsSetArgs) (Empty, error) {
	return Empty{}, Errorf(CodeUnsupported, "plugins.set 未实现：请编辑 profile.toml 后重启")
}

// ArtifactArgs artifact.get 参数。
type ArtifactArgs struct {
	Session string `json:"session"`
	Ref     string `json:"ref"`
}

// ArtifactGet 读工件：未实现。
func (a *App) ArtifactGet(_ context.Context, _ ArtifactArgs) (Empty, error) {
	return Empty{}, Errorf(CodeUnsupported, "artifact.get 未实现")
}
