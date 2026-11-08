package tenon

// SDK 公开面用到的类型（2026-11-04 审查 P1-52）：签名里只出现 tenon 的名字，应用不必 import 内核包，
// 07 7.6 的版本承诺按这些名字算。现在是别名（与内核类型相同，已有代码不用改）；内核类型改动时，
// 在这里换成 SDK 自己的定义 + 转换，不影响应用。

import (
	"github.com/Sevyn17/tenon/runtime/agentdef"
	"github.com/Sevyn17/tenon/runtime/assemble"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/modelhub"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
)

type (
	// Registrar 组件 Setup 时的登记入口（点位、服务、守卫规则）。
	Registrar = component.Registrar
	// Scope 组件 Start 时的作用域（后台工作与关闭回调）。
	Scope = component.Scope
	// Store 会话存储（WithStore；sqlite / memstore 或第三方实现）。
	Store = store.Store
	// Entry 会话历史里的一条记录（History 返回）。
	Entry = session.Entry
	// AgentSource 智能体定义来源（WithAgents、Dir）。
	AgentSource = agentdef.Source
	// ModelHub 模型调度（WithModels）。
	ModelHub = modelhub.Hub
	// UI 界面输出与提问（WithUI）。
	UI = assemble.UI
)
