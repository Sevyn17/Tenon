// Package appapi 应用接口层（docs/90 - 归档/10 - 框架模块/12 - 应用接口层.md）。
//
// CLI、桌面端（Wails）、api-server 都只调这一层。线上协议见 docs/06 - 应用协议契约.md：命令经 Call2(cmd, args JSON)
// 分发（进程内可直接调 V2() 的方法），传输层每个客户端连接一个 Conn（订阅、按身份过滤、背压，conn.go）。
// 进程内的消费者（CLI）也可以直接用 Events（易失）与 Follow（可靠，回放 + 实时）。
//
// 本包只依赖 core 包。权限档位、压缩等插件能力经小接口（Modes、Compactor）注入，
// 由应用用插件服务对象实现；appapi 不 import 任何插件包（runtime/deps_test.go）。
package appapi

import (
	"context"
	"errors"
	"sync"

	"ybase/runtime/agent"
	"ybase/runtime/assemble"
	"ybase/runtime/bus"
	"ybase/runtime/component"
	"ybase/runtime/host"
	"ybase/runtime/pkgs"
	"ybase/runtime/session"
	"ybase/runtime/store"
	"ybase/runtime/toolhub"
)

// Server hello 返回的服务端名。
const Server = "ybase-kernel"

// Options 应用装配好的部件。Host、Bus、DB 必填，其余可空。
type Options struct {
	Host *host.Host
	Bus  *bus.Bus
	DB   store.Store
	// Builder 会话构造器（可空）：非空时 New 在它的钩子上并入流式事件（StreamHooks）。
	// Host.New / Host.Child 由装配（assemble.Wire）或应用自己设置，appapi 不碰（2026-11-04 审查 P1-51）。
	Builder *assemble.Builder

	Model   string // 缺省模型名（hello 显示）
	Workdir string
	Trusted bool // 项目是否受信

	Modes     Modes              // 权限档位（permission 插件适配）；nil = 未装
	Compactor Compactor          // 手动压缩（compact-basic 适配）；nil = 未装
	Profile   *component.Profile // plugins.list 用；nil = 不提供
	Packages  []*pkgs.Package    // packages.list 用：已加载的插件包
	UI        *UI                // ask 的回答经 session.answer 送回；nil = 应用自己实现 assemble.UI
	// Artifacts 工件存储（protocol 2 artifact.get / put、超长工具结果的全文引用）；nil = 不提供。
	Artifacts toolhub.Artifacts
	// Profiles 可写的 profile 层（plugins.set）；nil = 只读（plugins.set 返回 unsupported）。
	Profiles Profiles
	// Files 列工作区文件（workspace.files，界面 @ 文件用）；nil = 不提供。
	Files Files
	// Cleaners 删除会话时依次调用的插件清理回调（on_session_delete 点位）；可空。
	Cleaners []store.Cleaner
}

// App 一个进程一个：持有装配结果，对外提供命令与事件。方法并发安全。
type App struct {
	o       Options
	streams sync.Map // 会话 id → *streamer
	lives   sync.Map // 会话 id → *liveState（protocol 2 分段流式与快照 live，stream2.go）
}

// New 构造 App：把 DB 的提交事件接到总线（Follow 的实时部分）；包一层 Host.Hooks
// （回合开始发档位变化提示 notice，回合结束先发完积压正文再交给原钩子）；给了 Builder 时在它的钩子上并入流式事件。
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
	if o.UI != nil {
		o.UI.bindHost(o.Host)
	}
	o.DB.OnCommit(o.Bus.Publish)

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
		if l := a.liveOf(id); l != nil { // 积压增量先于 turn.ended 发出，live 清空
			l.endTurn()
		}
		if after != nil {
			after(id, end)
		}
	}

	if rb := o.Builder; rb != nil {
		orig := rb.Hooks
		rb.Hooks = func(cx *assemble.SessionCx) agent.Hooks {
			hk := a.StreamHooks(cx.ID)
			if orig != nil {
				hk = MergeHooks(orig(cx), hk)
			}
			return hk
		}
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
