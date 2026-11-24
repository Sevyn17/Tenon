package appapi

// wsguard.go：会话不属于当前工作区时拒绝"会触发执行、改运行配置、改文件"的写命令（Mortise docs/71 §3.7、docs/72）。
//
// 多工作区并行之前，宿主只持有一个工作区（Host.Workspace）。界面可以只读查看别的工作区的会话，
// 但发消息、改档位、改模型、还原文件这些操作会在错的目录、错的项目配置下执行，必须先切换工作区。
// 只动会话元数据、不触发执行的操作（改标题、置顶、归档、删除、反馈、取消、移出队列、上传附件）照常放行：
// 数据页"清除全部会话 / 导出"会跨工作区调 session.delete、session.entries，不能被误伤。
//
// 判定：会话行 Workspace 非空且不等于 Host.Workspace 时拒绝；会话没记工作区（旧数据）或宿主没有工作区时放行。
// 错误：invalid，details.reason = other_workspace，details.workspace / workspace_id 供前端提示切换。

import (
	"context"

	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/runtime/host"
)

// inWorkspace 会话属于当前工作区（或无法判定为别处）时返回 nil；属于别的工作区返回 invalid（other_workspace）。
// 会话不存在时返回 nil，交给命令自己的存在性检查报 not_found。
func (v *App) inWorkspace(ctx context.Context, id string) error {
	cur := v.o.Host.Workspace
	if cur == "" || id == "" {
		return nil
	}
	l, err := host.LoadHead(ctx, v.o.DB, id)
	if err != nil {
		return nil
	}
	w := l.Meta.Workspace
	if w == "" || w == cur {
		return nil
	}
	e := Errorf(wire.CodeInvalid, "session %s belongs to workspace %s; switch to it before this operation", id, w)
	e.Details = map[string]any{"reason": wire.InvalidOtherWorkspace, "workspace": w, "workspace_id": wire.WorkspaceID(w)}
	return e
}
