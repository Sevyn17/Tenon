package appapi

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/runtime/store"
)

// docs/63 W-3：all_workspaces（与别的工作区的 workspace_id）只给本机操作者连接。
// viewer 连接被 viewerScope 拒；扩展命令即使在 operator 连接上、经 App.Call 进程内调核心命令，也不能跨工作区。
func TestAllWorkspacesOperatorOnly(t *testing.T) {
	var got []error
	probe := demoExt{ns: "x", cmds: map[string]ExtCommand{
		"x.probe.run": {Viewer: true, Handler: Bind(func(ctx context.Context, c ExtCall, _ wire.Empty) (wire.Empty, error) {
			got = got[:0]
			for _, q := range []struct{ cmd, args string }{
				{"sessions.list", `{"all_workspaces":true}`},
				{"sessions.list", `{"workspace_id":"` + wire.WorkspaceID("wb") + `"}`},
				{"session.get", `{"session":"other","all_workspaces":true}`},
				{"session.entries", `{"session":"other","all_workspaces":true}`},
			} {
				_, err := c.App.Call(ctx, q.cmd, json.RawMessage(q.args))
				got = append(got, err)
			}
			return wire.Empty{}, nil
		})},
	}}
	app := newAppOpts(t, func(o *Options) { o.Extensions = []Extension{probe} })
	app.o.Host.Workspace = "wa"
	storeSession(t, app, store.SessionMeta{ID: "here", Workspace: "wa"})
	storeSession(t, app, store.SessionMeta{ID: "other", Workspace: "wb"})
	ctx := context.Background()

	op := app.NewConn(ctx, LocalPrincipal)
	defer op.Close()
	if _, err := op.Call(ctx, "sessions.list", json.RawMessage(`{"all_workspaces":true}`)); err != nil {
		t.Fatalf("本机操作者连接应能用 all_workspaces：%v", err)
	}

	for _, p := range []wire.Principal{LocalPrincipal, {ID: "v", Role: wire.RoleViewer}} {
		c := app.NewConn(ctx, p)
		if _, err := c.Call(ctx, "x.probe.run", nil); err != nil {
			t.Fatalf("%s 调扩展命令：%v", p.Role, err)
		}
		c.Close()
		for i, err := range got {
			var e *Error
			if !errors.As(err, &e) || (e.Code != wire.CodeForbidden && e.Details["reason"] != wire.InvalidOtherWorkspace) {
				t.Fatalf("%s 扩展命令内第 %d 个跨工作区调用应被拒，得到 %v", p.Role, i, err)
			}
		}
	}
}
