package appapi

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/appapi/wire"
)

// fakeTruster 实现 Workspaces + Reloader + Truster：level 记在内存里，trusted = level == trusted。
type fakeTruster struct {
	fakeReloader
	level   string
	trusted bool // 当前运行时
	sets    []string
	setErr  error
}

func (f *fakeTruster) TrustPreview(context.Context) (wire.WorkspaceTrustPreviewResult, error) {
	return wire.WorkspaceTrustPreviewResult{Root: "w", Key: "w", Trusted: f.trusted, Level: f.level,
		MCPServers: []wire.TrustMCPServer{{Name: "fs", Command: "npx", Source: "project"}}}, nil
}

func (f *fakeTruster) SetTrust(_ context.Context, level string) (TrustChange, error) {
	if f.setErr != nil {
		return TrustChange{}, f.setErr
	}
	ch := TrustChange{Key: "w", Prev: f.level, Before: f.trusted, After: level == wire.TrustTrusted}
	f.level = level
	f.sets = append(f.sets, level)
	return ch, nil
}

func (f *fakeTruster) Reload(ctx context.Context) (wire.WorkspaceInfo, error) {
	ws, err := f.fakeReloader.Reload(ctx)
	if err == nil {
		f.trusted = f.level == wire.TrustTrusted
		ws.Trusted = f.trusted
	}
	return ws, err
}

// workspace.trustPreview / trust（客户端改进 M11 配置T13）：没有管理器或管理器不支持时 unsupported、不报能力；
// 支持时报能力；trust 写记录后重建、撤销后再重建；生效状态不变时不重建；level 校验；
// 有回合在跑时 not_idle 并改回记录；重建失败改回记录；viewer 能预览、不能 trust。
func TestV2WorkspaceTrust(t *testing.T) {
	old := reloadCancelWait
	reloadCancelWait = 3 * time.Second
	t.Cleanup(func() { reloadCancelWait = old })
	app := newApp(t)
	for _, cmd := range []string{"workspace.trustPreview", "workspace.trust"} {
		_, e := call2(t, app, cmd, `{}`)
		wantCode(t, e, wire.CodeUnsupported, "")
	}
	app.o.Workspaces = &fakeReloader{}
	if slices.Contains(capsOf(t, app), CapWorkspaceTrust) {
		t.Fatal("不支持信任的管理器不应报 workspace.trust")
	}
	_, e := call2(t, app, "workspace.trust", `{"level":"trusted"}`)
	wantCode(t, e, wire.CodeUnsupported, "")

	ft := &fakeTruster{}
	app.o.Workspaces = ft
	if !slices.Contains(capsOf(t, app), CapWorkspaceTrust) {
		t.Fatal("应报 workspace.trust 能力")
	}
	r, e := call2(t, app, "workspace.trustPreview", `{}`)
	if e != nil {
		t.Fatal(e)
	}
	pv := r.(wire.WorkspaceTrustPreviewResult)
	if pv.Trusted || len(pv.MCPServers) != 1 || pv.Hooks == nil || pv.WidenedModes == nil || pv.Packages == nil || pv.Plugins == nil || pv.Files == nil {
		t.Fatalf("预览：%+v", pv)
	}

	_, e = call2(t, app, "workspace.trust", `{"level":"maybe"}`)
	wantCode(t, e, wire.CodeInvalid, "")

	r, e = call2(t, app, "workspace.trust", `{"level":"trusted"}`)
	if e != nil {
		t.Fatal(e)
	}
	res := r.(wire.WorkspaceTrustResult)
	if !res.Reloaded || !res.Workspace.Trusted || res.Key != "w" || ft.reloads != 1 || !ft.trusted {
		t.Fatalf("信任后应重建：%+v reloads=%d", res, ft.reloads)
	}
	// 生效状态不变：只写记录，不重建
	r, e = call2(t, app, "workspace.trust", `{"level":"trusted"}`)
	if e != nil || r.(wire.WorkspaceTrustResult).Reloaded || ft.reloads != 1 {
		t.Fatalf("状态不变时不应重建：%+v %v %d", r, e, ft.reloads)
	}
	// 撤销
	r, e = call2(t, app, "workspace.trust", `{"level":""}`)
	if e != nil || !r.(wire.WorkspaceTrustResult).Reloaded || ft.trusted || ft.level != "" || ft.reloads != 2 {
		t.Fatalf("撤销后应按不受信重建：%+v %v trusted=%v", r, e, ft.trusted)
	}

	// 重建失败（回到原配置）：invalid + reverted，记录改回
	ft.err = &ReloadReverted{Err: errors.New("坏了")}
	_, e = call2(t, app, "workspace.trust", `{"level":"trusted"}`)
	wantCode(t, e, wire.CodeInvalid, "")
	if e.Details["reverted"] != true || ft.level != "" {
		t.Fatalf("重建失败应改回记录：%+v level=%q", e.Details, ft.level)
	}
	ft.err = nil

	ft.setErr = errors.New("磁盘满")
	_, e = call2(t, app, "workspace.trust", `{"level":"trusted"}`)
	wantCode(t, e, wire.CodeInternal, "")
	ft.setErr = nil

	// 有回合在跑：not_idle，记录改回、不重建
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
	before := ft.reloads
	_, e = call2(t, app, "workspace.trust", `{"level":"trusted"}`)
	wantCode(t, e, wire.CodeNotIdle, "")
	if ft.level != "" || ft.reloads != before {
		t.Fatalf("not_idle 时应改回记录、不重建：level=%q reloads=%d", ft.level, ft.reloads)
	}
	if r, e := call2(t, app, "workspace.trust", `{"level":"trusted","force":true}`); e != nil || !r.(wire.WorkspaceTrustResult).Reloaded || ft.reloads != before+1 {
		t.Fatalf("force 应先取消再重建：%v %d", e, ft.reloads)
	}
	if n, err := app.runningSessions(context.Background()); err != nil || n != 0 {
		t.Fatalf("force 之后不应还有回合在跑：%d %v", n, err)
	}

	// viewer：能预览，不能 trust
	v := app.NewConn(context.Background(), wire.Principal{ID: "v", Role: wire.RoleViewer})
	defer v.Close()
	if _, err := v.Call(context.Background(), "workspace.trustPreview", nil); err != nil {
		t.Fatalf("viewer 应能预览：%v", err)
	}
	_, err := v.Call(context.Background(), "workspace.trust", []byte(`{"level":"trusted"}`))
	if ae := AsError(err); ae == nil || ae.Code != wire.CodeForbidden {
		t.Fatalf("viewer 不能 trust：%v", err)
	}
}
