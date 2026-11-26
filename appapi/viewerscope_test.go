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
	// workspace_id（docs/74 W4a）：别的工作区视同 all_workspaces，viewer 拒绝；当前工作区照常；operator 按 id 列。
	if err := call(viewer, "sessions.list", `{"workspace_id":"`+wire.WorkspaceID("wb")+`"}`); code(err) != wire.CodeForbidden {
		t.Fatalf("viewer 带别的工作区 workspace_id 应 forbidden，得到 %v", err)
	}
	if err := call(viewer, "sessions.list", `{"workspace_id":"`+wire.WorkspaceID("wa")+`"}`); err != nil {
		t.Fatalf("viewer 带当前工作区 workspace_id 应照常：%v", err)
	}
	ids := func(c *Conn, args string) []string {
		t.Helper()
		raw, err := c.Call(ctx, "sessions.list", json.RawMessage(args))
		if err != nil {
			t.Fatalf("sessions.list %s：%v", args, err)
		}
		b, _ := json.Marshal(raw)
		var r wire.ListResult
		_ = json.Unmarshal(b, &r)
		var out []string
		for _, s := range r.Items {
			out = append(out, s.ID)
		}
		return out
	}
	if got := ids(op, `{"workspace_id":"`+wire.WorkspaceID("wb")+`"}`); len(got) != 1 || got[0] != "other" {
		t.Fatalf("operator 按别的工作区 id 列应只有 other，得到 %v", got)
	}
	if got := ids(op, `{"workspace_id":"`+wire.WorkspaceID("wa")+`"}`); len(got) != 1 || got[0] != "here" {
		t.Fatalf("operator 按当前工作区 id 列应只有 here，得到 %v", got)
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
		if err := call(op, cmd, `{"session":"other","all_workspaces":true}`); code(err) == wire.CodeForbidden {
			t.Fatalf("operator %s 带 all_workspaces 跨工作区读应放行：%v", cmd, err)
		}
		if err := call(viewer, cmd, `{"session":"other","all_workspaces":true}`); code(err) != wire.CodeForbidden {
			t.Fatalf("viewer %s 带 all_workspaces 仍应 forbidden，得到 %v", cmd, err)
		}
	}
}
