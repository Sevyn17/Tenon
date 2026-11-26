package appapi

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/runtime/store"
)

// docs/63 W-1：按会话 id 读会话内容的命令对所有身份（含 operator、进程内调用）统一只读当前工作区；
// 跨工作区读要显式带 all_workspaces，且只对 operator 生效。
func TestReadScope(t *testing.T) {
	app := newApp(t)
	app.o.Host.Workspace = "wa"
	storeSession(t, app, store.SessionMeta{ID: "here", Workspace: "wa"})
	storeSession(t, app, store.SessionMeta{ID: "other", Workspace: "wb"})

	ctx := context.Background()
	op := app.NewConn(ctx, LocalPrincipal)
	defer op.Close()

	reason := func(err error) any {
		var e *Error
		if errors.As(err, &e) && e.Code == wire.CodeInvalid {
			return e.Details["reason"]
		}
		return nil
	}
	reads := [][2]string{
		{"session.get", `{"session":"%s"}`},
		{"session.entries", `{"session":"%s"}`},
		{"session.stats", `{"session":"%s"}`},
		{"session.followups", `{"session":"%s"}`},
		{"session.subscribe", `{"session":"%s"}`},
		{"changes.list", `{"session":"%s"}`},
		{"changes.diff", `{"session":"%s","path":"a"}`},
		{"artifact.get", `{"session":"%s","ref":"x"}`},
	}
	arg := func(tmpl, id string, all bool) json.RawMessage {
		var m map[string]any
		_ = json.Unmarshal([]byte(tmpl), &m)
		m["session"] = id
		if all {
			m["all_workspaces"] = true
		}
		b, _ := json.Marshal(m)
		return b
	}
	for _, r := range reads {
		cmd := r[0]
		if _, err := op.Call(ctx, cmd, arg(r[1], "other", false)); reason(err) != wire.InvalidOtherWorkspace {
			t.Fatalf("operator %s 别的工作区会话应 other_workspace，得到 %v", cmd, err)
		}
		if _, err := app.Call(ctx, cmd, arg(r[1], "other", false)); cmd != "session.subscribe" && reason(err) != wire.InvalidOtherWorkspace {
			t.Fatalf("进程内 %s 别的工作区会话应 other_workspace，得到 %v", cmd, err)
		}
		if _, err := op.Call(ctx, cmd, arg(r[1], "other", true)); reason(err) == wire.InvalidOtherWorkspace {
			t.Fatalf("operator %s 带 all_workspaces 应放行：%v", cmd, err)
		}
		if _, err := op.Call(ctx, cmd, arg(r[1], "here", false)); reason(err) == wire.InvalidOtherWorkspace {
			t.Fatalf("operator %s 本工作区会话不应被拒：%v", cmd, err)
		}
	}
	// fork：来源在别的工作区一律拒绝（不受 all_workspaces 影响：分叉会在本工作区执行），且不留会话行。
	if _, err := op.Call(ctx, "session.fork", json.RawMessage(`{"session":"other","to":"f1"}`)); reason(err) != wire.InvalidOtherWorkspace {
		t.Fatalf("operator 跨工作区 fork 应 other_workspace，得到 %v", err)
	}
	if ok, _ := app.o.DB.Exists(ctx, "f1"); ok {
		t.Fatal("跨工作区 fork 被拒后不应留下会话行")
	}
}
