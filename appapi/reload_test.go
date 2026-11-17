package appapi

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/appapi/wire"
)

// fakeReloader 实现 Workspaces + Reloader；err 非空时 Reload 返回它。
type fakeReloader struct {
	fakeWorkspaces
	reloads int
	err     error
}

func (f *fakeReloader) Reload(context.Context) (wire.WorkspaceInfo, error) {
	f.reloads++
	if f.err != nil {
		return wire.WorkspaceInfo{}, f.err
	}
	return wire.WorkspaceInfo{Path: "w", Name: "w", Current: true}, nil
}

func capsOf(t *testing.T, app *App) []string {
	t.Helper()
	r, e := call2(t, app, "hello", `{}`)
	if e != nil {
		t.Fatal(e)
	}
	return r.(wire.HelloResult).Capabilities
}

// workspace.reload（客户端改进 M3）：管理器不支持重建时 unsupported 且不报 reload 能力；支持时报能力、成功重建；
// 有回合在跑时 not_idle 并列出会话，force 先取消回合再重建；回到原配置的失败是 invalid + reverted，其余是 internal。
func TestV2WorkspaceReload(t *testing.T) {
	old := reloadCancelWait
	reloadCancelWait = 3 * time.Second
	t.Cleanup(func() { reloadCancelWait = old })

	app := newApp(t)
	_, e := call2(t, app, "workspace.reload", `{}`)
	wantCode(t, e, wire.CodeUnsupported, "")

	app.o.Workspaces = &fakeWorkspaces{}
	if slices.Contains(capsOf(t, app), "reload") {
		t.Fatal("只能切换、不能重建的管理器不应报 reload")
	}
	_, e = call2(t, app, "workspace.reload", `{}`)
	wantCode(t, e, wire.CodeUnsupported, "")

	rl := &fakeReloader{}
	app.o.Workspaces = rl
	if !slices.Contains(capsOf(t, app), "reload") {
		t.Fatal("能重建时应报 reload 能力")
	}
	r, e := call2(t, app, "workspace.reload", `{}`)
	if e != nil || rl.reloads != 1 || r.(wire.WorkspaceReloadResult).Workspace.Path != "w" {
		t.Fatalf("空闲时应直接重建：%+v %v %d", r, e, rl.reloads)
	}

	// 回合停在工具确认上：不带 force 拒绝，带 force 取消后重建
	if _, e := call2(t, app, "session.create", `{"session":"s"}`); e != nil {
		t.Fatal(e)
	}
	c := app.NewConn(context.Background(), LocalPrincipal)
	defer c.Close()
	if err := c.Subscribe(context.Background(), wire.SubscribeArgs{Session: "s"}); err != nil {
		t.Fatal(err)
	}
	waitKind(t, c, wire.EvSynced)
	if _, e := call2(t, app, "session.send", `{"session":"s","text":"call tool","client_id":"c1"}`); e != nil {
		t.Fatal(e)
	}
	waitKind(t, c, wire.EvInteractionOpen)
	_, e = call2(t, app, "workspace.reload", `{}`)
	wantCode(t, e, wire.CodeNotIdle, "")
	if ids, _ := e.Details["sessions"].([]string); !slices.Equal(ids, []string{"s"}) || e.Details["running"] != 1 {
		t.Fatalf("not_idle 应列出正在跑的会话：%+v", e.Details)
	}
	if rl.reloads != 1 {
		t.Fatal("回合进行中不带 force 不应重建")
	}
	if _, e := call2(t, app, "workspace.reload", `{"force":true}`); e != nil || rl.reloads != 2 {
		t.Fatalf("force 应先取消再重建：%v %d", e, rl.reloads)
	}
	if n, err := app.runningSessions(context.Background()); err != nil || n != 0 {
		t.Fatalf("force 之后不应还有回合在跑：%d %v", n, err)
	}

	rl.err = &ReloadReverted{Err: errors.New("插件 x Setup: 坏了")}
	_, e = call2(t, app, "workspace.reload", `{}`)
	wantCode(t, e, wire.CodeInvalid, "")
	if e.Details["reverted"] != true {
		t.Fatalf("回到原配置应带 details.reverted：%+v", e.Details)
	}
	rl.err = errors.New("回也回不去")
	_, e = call2(t, app, "workspace.reload", `{}`)
	wantCode(t, e, wire.CodeInternal, "")
}
