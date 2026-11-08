package tenon

// 应用宿主选项（godoc 的"应用宿主选项"一组）与内核对象（计划/07 阶段三；计划/08 3.4 把访问器收成 Internals）：按 profile 装配、插件提供存储、注入应用预置服务、会话钩子、
// 档位确认记录、协议层选项。apphost（profile 分层、项目信任、工作区）建在这些之上；
// 自己写产品外壳的应用也可以直接用。只用 Ask / History 的嵌入方用不到这一页。

import (
	"fmt"

	"tenon/adapters"
	"tenon/appapi"
	"tenon/runtime/agent"
	"tenon/runtime/agentdef"
	"tenon/runtime/assemble"
	"tenon/runtime/bus"
	"tenon/runtime/component"
	"tenon/runtime/host"
	"tenon/runtime/store"
	"tenon/runtime/toolhub"
)

// WithProfile 按 profile 装配：开哪些组件按各组件声明的缺省（Meta.Default）加 profile 里的开关，
// 组件配置取 profile 的 [plugin.<名>] 节，内核参数取 [kernel] 节（WithKernel 再覆盖它）。
// 与 Default() / Minimal() / Without 二选一。profile 里有提供 service:db 的组件（如 store-sqlite）时
// 不必 WithStore。
func WithProfile(p *component.Profile) Option {
	return func(c *config) error {
		if p == nil {
			return fmt.Errorf("%w: WithProfile 传了 nil", ErrInvalid)
		}
		c.profile = p
		return nil
	}
}

// reserved 由别的选项管的服务键：用 WithService 设它们会和对应选项冲突。
var reserved = map[string]string{
	assemble.ServiceHost:    "（SDK 自己建宿主）",
	assemble.ServiceUI:      "WithUI / WithProtocolUI",
	assemble.ServiceWorkdir: "WithWorkdir",
	assemble.ServiceTrust:   "WithProjectTrust",
	assemble.ServiceDB:      "WithStore",
}

// WithService 给组件预置一项服务（装配时按服务键注入，如插件包列表 pkgs.Service）。
// 宿主、界面、工作目录、信任、存储由各自的选项管，不能用它设。
func WithService(key string, v any) Option {
	return func(c *config) error {
		if key == "" || v == nil {
			return fmt.Errorf("%w: WithService 的键或值为空", ErrInvalid)
		}
		if by, ok := reserved[key]; ok {
			return fmt.Errorf("%w: 服务 %s 用 %s 设", ErrInvalid, key, by)
		}
		if c.services == nil {
			c.services = map[string]any{}
		}
		c.services[key] = v
		return nil
	}
}

// WithWorkspace 本应用所在工作区的键（如规范化后的项目目录）。新建的会话记上它，会话列表只列它的，
// 打开属于别的工作区的会话被拒绝（会话的工具在本应用的工作目录里执行）。多个工作区共用一个存储时要设。
// 空（缺省）= 不区分工作区。
func WithWorkspace(key string) Option {
	return func(c *config) error {
		c.workspace = key
		return nil
	}
}

// WithSessionHooks 应用自己的会话钩子（如终端打印正文与工具调用）；协议层的流式事件钩子总会并入。
func WithSessionHooks(f func(*assemble.SessionCx) agent.Hooks) Option {
	return func(c *config) error {
		c.hooks = f
		return nil
	}
}

// WithModeApprovals 项目档位放宽权限的确认记录（mode.set 与新会话缺省档位共用）；缺省不记，每次都要确认。
func WithModeApprovals(a adapters.ModeApprovals) Option {
	return func(c *config) error {
		c.approvals = a
		return nil
	}
}

// WithProtocolOptions 在接上插件能力之前改协议层的选项：plugins.list 用的 profile、可写的 profile 层、
// 工作区切换、传输层是否挂了上传等。可以给多个，按顺序执行；已经设了的能力 adapters 不再覆盖。
func WithProtocolOptions(f func(*appapi.Options)) Option {
	return func(c *config) error {
		if f == nil {
			return fmt.Errorf("%w: WithProtocolOptions 传了 nil", ErrInvalid)
		}
		c.protocol = append(c.protocol, f)
		return nil
	}
}

// WithReadyErrors ready 点位（如工作组补投）出错时交给 f，不让 Start 失败；缺省任何一个出错 Start 都失败。
// 应用宿主用它把这类错误当提示显示。
func WithReadyErrors(f func([]error)) Option {
	return func(c *config) error {
		c.readyErrs = f
		return nil
	}
}

// Internals 内核对象：给建在 SDK 上的应用宿主用（列插件、查轨迹、打开会话等）。
//
// 这些类型来自 runtime/*，不在稳定承诺之内（见 docs/框架设计/07 - SDK 接口.md）：升级时可能改名、改签名。
// 只用 Ask / Session / API 的应用用不到它。
type Internals struct {
	Host        *host.Host          // 会话宿主
	Bus         *bus.Bus            // 事件总线
	Assembly    *component.Assembly // 装配结果（组件、点位、服务）
	Models      *assemble.Models    // 模型服务；没装 models 组件时为 nil
	Definitions agentdef.Source     // 智能体定义来源（组件模板 + 应用来源）
	DB          store.Store         // 存储；存储由组件提供时 Open / Start 之前为 nil
	Blobs       toolhub.Artifacts   // 工件存储；Open / Start 之前可能为 nil
	Builder     *assemble.Builder   // 会话构造器；Start 之前为 nil
}

// Internals 取内核对象的快照：DB、Blobs、Builder 在 Open / Start 之后才有，之前取到的是 nil，要用时重新取。
func (a *App) Internals() Internals {
	in := Internals{Host: a.host, Bus: a.bus, Assembly: a.asm, Models: a.models, Definitions: a.defs}
	if a.handle != nil {
		in.DB, in.Blobs = a.handle.DB(), a.handle.Blobs()
	}
	a.mu.Lock()
	in.Builder = a.builder
	a.mu.Unlock()
	return in
}
