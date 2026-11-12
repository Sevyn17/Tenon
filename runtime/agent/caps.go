package agent

// 本轮的工具授权范围（信封 caps，框架设计 08 8.x / 13 13.3）：
// 会话可能由别的会话或组件触发，触发链上每一跳的 caps 取交集；nil 表示不限，空集表示一个都不许
// （与 host.intersectCaps 一致）。
// 这里用一层 Guard 在工具执行前再拦一次：模型请求了不在范围里的工具时返回错误结果，
// 不交给执行器（权限判定由权限组件的 Authorizer 负责，见 toolhub.Hub.Validate）。

import (
	"context"
	"encoding/json"

	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// InstallCapsGuard 给本会话的工具栈装一层 caps 守卫（只装一次；Host 在会话打开时调用）。
func (a *Agent) InstallCapsGuard() {
	if a == nil || a.Hub == nil || !a.caps.markGuarded() {
		return
	}
	// 04 4.3.2：caps 是**纯判定**（看工具名在不在本轮授权范围），不改参数、不问人，
	// 所以是 Gate 而不是 Transform——判定可并行、永远 fail-closed。
	a.Hub.AddGate(toolhub.Gate{
		Name: "caps",
		Check: func(ctx context.Context, cx *toolhub.Cx, args json.RawMessage) (toolhub.Verdict, string) {
			if name := cx.Tool.Spec.Name; !a.caps.allows(name) {
				return toolhub.VerdictDeny, "工具 " + name + " 不在本轮授权范围（caps）"
			}
			return toolhub.VerdictAbstain, "" // 不表态，交给其它判定与内核
		},
	})
}

// SetCaps 设置本轮的工具授权范围：nil = 不限，空集 = 一个都不许（下一回合由 host 重设）。
func (a *Agent) SetCaps(caps []string) {
	if a == nil {
		return
	}
	a.caps.set(caps)
}

// Caps 当前本轮的授权范围（拷贝；nil = 不限，空集 = 一个都不许）。
func (a *Agent) Caps() []string {
	if a == nil {
		return nil
	}
	return a.caps.get()
}
