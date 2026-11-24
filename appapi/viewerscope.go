package appapi

// viewerscope.go：viewer 只能看当前工作区（Mortise docs/74 第二版 W1a，W-3）。
//
// 会话库是用户级的，多个工作区共用；operator 是本机操作者，可以跨工作区读（数据页导出、智能体页汇总）。
// viewer 是受限身份（远程只读观察者等），只给当前工作区：
//   - sessions.list 带 all_workspaces → forbidden；
//   - 按会话读的只读命令（session.get / entries / stats / followups / subscribe、changes.list / diff、
//     interactions.list、artifact.get）
//     指向别的工作区的会话 → forbidden。
// 判定复用 wsguard.go 的 inWorkspace：会话没记工作区（旧数据）、宿主没有工作区、会话不存在时放行，
// 不存在的交给命令自己报 not_found。

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Sevyn17/tenon/appapi/wire"
)

// viewerSessionReads viewer 可调、且按 session 参数读取一个会话的命令。
var viewerSessionReads = map[string]bool{
	"session.get": true, "session.entries": true, "session.stats": true, "session.followups": true,
	"changes.list": true, "changes.diff": true, "interactions.list": true,
	"session.subscribe": true, "artifact.get": true, // 订阅推送条目、按会话取工件，同样是读会话内容
}

// viewerScope 非 operator 的调用在进入命令前检查工作区范围；越界返回 forbidden。
func (v *App) viewerScope(ctx context.Context, p wire.Principal, cmd string, args json.RawMessage) error {
	var in struct {
		Session       string `json:"session"`
		AllWorkspaces bool   `json:"all_workspaces"`
	}
	if len(args) > 0 {
		_ = json.Unmarshal(args, &in) // 解析失败交给命令自己报参数错误
	}
	switch {
	case cmd == "sessions.list" && in.AllWorkspaces:
		return Errorf(wire.CodeForbidden, "principal %s (%s) may not list sessions of all workspaces", p.ID, p.Role)
	case viewerSessionReads[cmd] && in.Session != "":
		if err := v.inWorkspace(ctx, in.Session); err != nil {
			var e *Error
			if errors.As(err, &e) && e.Code == wire.CodeInvalid {
				f := Errorf(wire.CodeForbidden, "principal %s (%s) may not read session %s of another workspace", p.ID, p.Role, in.Session)
				f.Details = e.Details
				return f
			}
			return err
		}
	}
	return nil
}
