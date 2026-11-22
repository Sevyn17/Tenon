package apphost

// 进程内取当前工作区的 App（docs/57 4.2 钩子 1，docs/61 T0）：自动化调度器等进程内消费者用它直接调
// session.create / mode.set / session.send（App.Call），并用 App.Subscribe 等回合结束，不经协议连接。
// 切换工作区、重建运行时后返回的是新的 App，所以每次用时现取，不要缓存。

import "github.com/Sevyn17/tenon/appapi"

// CurrentApp 当前工作区的应用接口层；没有当前工作区（已关闭、重建失败且回不去）时为 nil。
func (m *WorkspaceManager) CurrentApp() *appapi.App {
	ws := m.Current()
	if ws == nil || ws.Built == nil {
		return nil
	}
	return ws.Built.API
}
