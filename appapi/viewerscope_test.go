package appapi

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/runtime/store"
)

// viewer 只能看当前工作区（Mortise docs/74 第二版 W1a）：all_workspaces 与按会话读别的工作区的会话被拒；
// operator 不受限；不带参数的 viewer 列表照常。
func TestViewerScope(t *testing.T) {
	app := newApp(t)
	app.o.Host.Workspace = "wa"
	storeSession(t, app, store.SessionMeta{ID: "here", Workspace: "wa"})
	storeSession(t, app, store.SessionMeta{ID: "other", Workspace: "wb"})
	storeSession(t, app, store.SessionMeta{ID: "legacy"})

	ctx := context.Background()
	viewer := app.NewConn(ctx, wire.Principal{ID: "v", Role: wire.RoleViewer})
	defer viewer.Close()
	op := app.NewConn(ctx, wire.Principal{ID: "o", Role: wire.RoleOperator})
	defer op.Close()

	code := func(err error) string {
		var e *Error
		if errors.As(err, &e) {
			return e.Code
		}
		return ""
	}
	call := func(c *Conn, cmd, args string) error {
		_, err := c.Call(ctx, cmd, json.RawMessage(args))
		return err
	}

	if err := call(viewer, "sessions.list", `{"all_workspaces":true}`); code(err) != wire.CodeForbidden {
		t.Fatalf("viewer 带 all_workspaces 应 forbidden，得到 %v", err)
	}
	if err := call(viewer, "sessions.list", `{}`); err != nil {
		t.Fatalf("viewer 不带参数应照常：%v", err)
	}
	if err := call(op, "sessions.list", `{"all_workspaces":true}`); err != nil {
		t.Fatalf("operator 带 all_workspaces 应放行：%v", err)
	}

	for _, cmd := range []string{"session.get", "session.entries", "session.stats"} {
		err := call(viewer, cmd, `{"session":"other"}`)
		var e *Error
		if !errors.As(err, &e) || e.Code != wire.CodeForbidden || e.Details["workspace"] != "wb" {
			t.Fatalf("viewer %s 别的工作区会话应 forbidden 并带工作区，得到 %v", cmd, err)
		}
		if err := call(viewer, cmd, `{"session":"here"}`); code(err) == wire.CodeForbidden {
			t.Fatalf("viewer %s 本工作区会话不应被拒：%v", cmd, err)
		}
		if err := call(viewer, cmd, `{"session":"legacy"}`); code(err) == wire.CodeForbidden {
			t.Fatalf("viewer %s 旧会话（无工作区）不应被拒：%v", cmd, err)
		}
		if err := call(op, cmd, `{"session":"other"}`); code(err) == wire.CodeForbidden {
			t.Fatalf("operator %s 跨工作区读应放行：%v", cmd, err)
		}
	}
}
