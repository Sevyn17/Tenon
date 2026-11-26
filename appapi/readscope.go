package appapi

// readscope.go：按会话 id 读会话内容的命令一律只读当前工作区（docs/63 W-1）。
//
// 会话库是用户级的，多个工作区共用。以前只有 viewer 在 viewerScope 里检查工作区，operator 的
// session.get / entries 等可以读任何工作区的会话。现在所有身份统一检查：会话工作区 ≠ Host.Workspace 时
// 返回 invalid（details.reason = other_workspace，同 inWorkspace）。
//
// 跨工作区读只能显式请求：参数带 all_workspaces = true，且调用方是 operator（数据页导出等，同 CLI export -all，
// docs/63 W-5）。viewer 带 all_workspaces 仍由 viewerScope 拒绝（forbidden）。
//
// 调用方身份经 ctx 传入（withPrincipal）：连接上是连接身份，进程内 App.Call 没有身份时按 LocalPrincipal。

import (
	"context"
	"encoding/json"

	"github.com/Sevyn17/tenon/appapi/wire"
)

// sessionReads 按 session 参数读取一个会话内容的命令（session.subscribe 在 Conn 里处理，同样检查）。
// session.locate 不在此列：它只回答「会话在哪个工作区」，本就给 operator 跨工作区用。
var sessionReads = map[string]bool{
	"session.get": true, "session.entries": true, "session.stats": true, "session.followups": true,
	"session.subscribe": true, "changes.list": true, "changes.diff": true, "artifact.get": true,
}

type principalKey struct{}

// withPrincipal 把调用方身份放进 ctx，供 readScope 判定 all_workspaces 是否生效。
func withPrincipal(ctx context.Context, p wire.Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// principalOf ctx 里的调用方身份；没有时是 LocalPrincipal（进程内调用）。
func principalOf(ctx context.Context) wire.Principal {
	if p, ok := ctx.Value(principalKey{}).(wire.Principal); ok {
		return p
	}
	return LocalPrincipal
}

type noAllKey struct{}

// withoutAllWorkspaces 标记 ctx 来自扩展命令（docs/63 W-3）：其中再经 App.Call 进程内调核心命令时，
// 身份虽是 LocalPrincipal，all_workspaces / 别的工作区 workspace_id 一律不生效。
func withoutAllWorkspaces(ctx context.Context) context.Context {
	return context.WithValue(ctx, noAllKey{}, true)
}

// mayAllWorkspaces 调用方能否用 all_workspaces 跨工作区读：只有本机操作者连接（operator）与
// 非扩展的进程内调用可以；viewer、插件界面以外的受限身份、扩展命令都不行（docs/63 W-3）。
func mayAllWorkspaces(ctx context.Context) bool {
	if no, _ := ctx.Value(noAllKey{}).(bool); no {
		return false
	}
	return principalOf(ctx).Role == wire.RoleOperator
}

// readScope 会话读取命令进入前的工作区检查；不是读取命令时返回 nil。
func (v *App) readScope(ctx context.Context, cmd string, args json.RawMessage) error {
	if !sessionReads[cmd] {
		return nil
	}
	var in struct {
		Session       string `json:"session"`
		AllWorkspaces bool   `json:"all_workspaces"`
	}
	if len(args) > 0 {
		_ = json.Unmarshal(args, &in) // 解析失败交给命令自己报参数错误
	}
	if in.AllWorkspaces && mayAllWorkspaces(ctx) {
		return nil
	}
	return v.inWorkspace(ctx, in.Session)
}
