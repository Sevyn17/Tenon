package agent

// 本轮的工具授权范围（信封 caps，框架设计 08 8.x / 13 13.3）：
// 会话可能由别的会话或组件触发，触发链上每一跳的 caps 取交集；交集为空表示不限。
// 这里用一层 Guard 在工具执行前再拦一次：模型请求了不在范围里的工具时返回错误结果，
// 不交给执行器（权限判定仍由权限插件的 Authorizes 层负责，见 toolhub.Validate）。

import (
	"context"
	"encoding/json"

	"ybase/runtime/toolhub"
)

// InstallCapsGuard 给本会话的工具栈装一层 caps 守卫（只装一次；Host 在会话打开时调用）。
func (a *Agent) InstallCapsGuard() {
	if a == nil || a.Hub == nil {
		return
	}
	a.capsMu.Lock()
	if a.capsGuarded {
		a.capsMu.Unlock()
		return
	}
	a.capsGuarded = true
	a.capsMu.Unlock()
	a.Hub.PrependLayer(toolhub.Layer{
		Name:  "caps",
		Guard: true,
		Call: func(ctx context.Context, cx *toolhub.Cx, args json.RawMessage, next toolhub.Next) (toolhub.Output, error) {
			if name := cx.Tool.Spec.Name; !a.allowedByCaps(name) {
				return toolhub.Errorf("工具 %s 不在本轮授权范围（caps）", name), nil
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
	a.capsMu.Lock()
	if caps == nil {
		a.caps = nil
	} else {
		a.caps = append([]string{}, caps...)
	}
	a.capsMu.Unlock()
}

// Caps 当前本轮的授权范围（拷贝；nil = 不限，空集 = 一个都不许）。
func (a *Agent) Caps() []string {
	if a == nil {
		return nil
	}
	a.capsMu.RLock()
	defer a.capsMu.RUnlock()
	if a.caps == nil {
		return nil
	}
	return append([]string{}, a.caps...)
}

// allowedByCaps 工具是否在本轮范围内；没有范围（nil）时全部允许，空集时全部拒绝。
func (a *Agent) allowedByCaps(tool string) bool {
	a.capsMu.RLock()
	defer a.capsMu.RUnlock()
	if a.caps == nil {
		return true
	}
	for _, c := range a.caps {
		if c == tool {
			return true
		}
	}
	return false
}
