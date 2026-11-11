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
	a.Hub.PrependLayer(toolhub.Layer{
		Name:  "caps",
		Guard: true,
		Call: func(ctx context.Context, cx *toolhub.Cx, args json.RawMessage, next toolhub.Next) (toolhub.Output, error) {
			if name := cx.Tool.Spec.Name; !a.caps.allows(name) {
				return toolhub.Failf("工具 %s 不在本轮授权范围（caps）", name), nil
			}
			return next(ctx, cx, args)
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
