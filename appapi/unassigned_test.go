package appapi

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
)

// 客户端改进 M6：CLI 走 Host.Open 建的会话（协议T5）出现在缺省（按工作区）的 sessions.list 里；
// 没有工作区的旧会话只在 {unassigned} 里（决定 10，左栏「未归属」分组），置顶查询同样按它过滤。
func TestSessionsListWorkspaceAndUnassigned(t *testing.T) {
	ctx := context.Background()
	app := newApp(t)
	db := app.o.DB
	app.o.Host.Workspace = "wa"

	if _, err := app.o.Host.Open(ctx, "cli-1"); err != nil { // CLI 的建会话路径
		t.Fatal(err)
	}
	hdr := session.New(&session.Config{Model: "m", Policy: session.DefaultPolicy()}).Snapshot().Header
	for _, m := range []store.SessionMeta{{ID: "old"}, {ID: "old-pin"}, {ID: "other", Workspace: "wb"}} {
		if err := db.CreateSession(ctx, m, hdr, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.(store.SessionMetaStore).UpdateSessionMeta(ctx, "old-pin", nil, map[string]any{metaPinned: true}); err != nil {
		t.Fatal(err)
	}

	ids := func(a wire.ListArgs) []string {
		t.Helper()
		r, err := app.SessionsList(ctx, a)
		if err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, x := range r.Items {
			out = append(out, x.ID)
		}
		slices.Sort(out)
		return out
	}
	if got := ids(wire.ListArgs{}); !slices.Equal(got, []string{"cli-1"}) {
		t.Fatalf("缺省列表应只有本工作区的 cli-1：%v", got)
	}
	if got := ids(wire.ListArgs{Unassigned: true}); !slices.Equal(got, []string{"old", "old-pin"}) {
		t.Fatalf("unassigned 应只有没有工作区的会话：%v", got)
	}
	if got := ids(wire.ListArgs{Unassigned: true, AllWorkspaces: true}); !slices.Equal(got, []string{"old", "old-pin"}) {
		t.Fatalf("unassigned 优先于 all_workspaces：%v", got)
	}
	if got := ids(wire.ListArgs{Unassigned: true, Pinned: true}); !slices.Equal(got, []string{"old-pin"}) {
		t.Fatalf("置顶查询 + unassigned：%v", got)
	}
	if got := ids(wire.ListArgs{Pinned: true}); len(got) != 0 {
		t.Fatalf("缺省置顶查询不应含别的工作区或未归属的会话：%v", got)
	}
	if got := ids(wire.ListArgs{AllWorkspaces: true}); len(got) != 4 {
		t.Fatalf("all_workspaces 应列全部：%v", got)
	}

	if !slices.Contains(app.capabilities(), "sessions_unassigned") {
		t.Fatal("宿主有工作区、存储实现了 UnassignedLister：应报 sessions_unassigned")
	}
	app.o.Host.Workspace = ""
	if slices.Contains(app.capabilities(), "sessions_unassigned") {
		t.Fatal("宿主没有工作区时列表不过滤，不应报 sessions_unassigned")
	}
}

// 存储没实现 UnassignedLister：{unassigned} 返回 unsupported，不退回成别的列表。
func TestSessionsListUnassignedUnsupported(t *testing.T) {
	app := newApp(t)
	app.o.DB = plainStore{app.o.DB}
	app.o.Host.Workspace = "wa"
	_, err := app.SessionsList(context.Background(), wire.ListArgs{Unassigned: true})
	var ae *Error
	if !errors.As(err, &ae) || ae.Code != wire.CodeUnsupported {
		t.Fatalf("应返回 unsupported，得到 %v", err)
	}
	if slices.Contains(app.capabilities(), "sessions_unassigned") {
		t.Fatal("存储不支持时不应报能力")
	}
}

// plainStore 只暴露 store.Store（隐藏 UnassignedLister 等可选接口）。
type plainStore struct{ store.Store }
