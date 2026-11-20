package appapi

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/pkgs"
)

// fakePkgs 实现 Workspaces + Reloader + PackageManager。
type fakePkgs struct {
	fakeReloader
	calls  []string
	undone int
	user   []wire.PackageInfo
}

func (f *fakePkgs) UserPackages(context.Context) ([]wire.PackageInfo, error) { return f.user, nil }

func (f *fakePkgs) PreviewPackage(_ context.Context, src string) (wire.PackagesPreviewResult, error) {
	f.calls = append(f.calls, "preview:"+src)
	if src == "bad" {
		return wire.PackagesPreviewResult{}, Errorf(wire.CodeInvalid, "清单坏了")
	}
	return wire.PackagesPreviewResult{Name: "demo"}, nil
}

func (f *fakePkgs) write(op string) (PackageWrite, error) {
	f.calls = append(f.calls, op)
	return PackageWrite{Name: "demo", Trashed: "t", Changed: op != "enable:same", Undo: func() error { f.undone++; return nil }}, nil
}

func (f *fakePkgs) InstallPackage(_ context.Context, src string) (PackageWrite, error) {
	return f.write("install:" + src)
}
func (f *fakePkgs) RemovePackage(_ context.Context, name string) (PackageWrite, error) {
	return f.write("remove:" + name)
}
func (f *fakePkgs) SetPackageEnabled(_ context.Context, name string, on bool) (PackageWrite, error) {
	if name == "same" {
		return f.write("enable:same")
	}
	return f.write("enable:" + name)
}

// 没有管理器：只有 packages.list 可用（enabled = true），其余 unsupported，不报能力。
func TestPackagesUnsupported(t *testing.T) {
	app := newAppOpts(t, func(o *Options) {
		o.Packages = []*pkgs.Package{{Manifest: pkgs.Manifest{Name: "p", Version: "1"}, Scope: pkgs.ScopeUser, Dir: "/d"}}
	})
	if slices.Contains(capsOf(t, app), CapPackagesManage) {
		t.Fatal("没有管理器不应报 packages.manage")
	}
	r, e := call2(t, app, "packages.list", `{}`)
	if e != nil {
		t.Fatal(e)
	}
	if l := r.(wire.PackagesListResult); len(l.Packages) != 1 || !l.Packages[0].Enabled || l.Packages[0].Managed || l.Packages[0].Dir != "/d" || l.Packages[0].Risks == nil {
		t.Fatalf("list：%+v", l)
	}
	for _, cmd := range []string{"packages.preview", "packages.install", "packages.remove", "packages.setEnabled"} {
		_, e := call2(t, app, cmd, `{"source":"x","name":"x"}`)
		wantCode(t, e, wire.CodeUnsupported, "")
	}
}

// 能力、参数校验、list 合并用户级目录里没加载的包、状态没变不重建、重建失败调 Undo、not_idle / force、viewer 只能 list。
func TestPackagesCommands(t *testing.T) {
	old := reloadCancelWait
	reloadCancelWait = 3 * time.Second
	t.Cleanup(func() { reloadCancelWait = old })

	fp := &fakePkgs{user: []wire.PackageInfo{
		{Name: "loaded", Enabled: true},
		{Name: "off", Enabled: false, Contents: "技能"},
		{Name: "broken", Enabled: true, Error: "没有 tenon-plugin.toml"},
		{Name: "shadow", Enabled: true},
	}}
	app := newAppOpts(t, func(o *Options) {
		o.Workspaces = fp
		o.Packages = []*pkgs.Package{
			{Manifest: pkgs.Manifest{Name: "loaded"}, Scope: pkgs.ScopeUser},
			{Manifest: pkgs.Manifest{Name: "shadow"}, Scope: pkgs.ScopeProject},
		}
	})
	if !slices.Contains(capsOf(t, app), CapPackagesManage) {
		t.Fatal("应报 packages.manage")
	}
	r, e := call2(t, app, "packages.list", `{}`)
	if e != nil {
		t.Fatal(e)
	}
	got := map[string]wire.PackageInfo{}
	var order []string
	for _, p := range r.(wire.PackagesListResult).Packages {
		got[p.Name+"@"+p.Scope] = p
		order = append(order, p.Name+"@"+p.Scope)
	}
	if !slices.Equal(order, []string{"loaded@user", "shadow@project", "off@user", "broken@user", "shadow@user"}) {
		t.Fatalf("顺序：%v", order)
	}
	if p := got["loaded@user"]; !p.Enabled || !p.Managed {
		t.Fatalf("已加载的用户包：%+v", p)
	}
	if p := got["shadow@project"]; p.Managed {
		t.Fatal("项目包不可管理")
	}
	if p := got["off@user"]; p.Enabled || !p.Managed || p.Error != "" {
		t.Fatalf("停用包：%+v", p)
	}
	if p := got["broken@user"]; p.Enabled || p.Error == "" {
		t.Fatalf("坏包：%+v", p)
	}
	if p := got["shadow@user"]; p.Enabled || p.Error == "" {
		t.Fatalf("被项目包覆盖的用户包：%+v", p)
	}

	// 参数校验与 invalid 透传
	for _, c := range []struct{ cmd, args, code string }{
		{"packages.preview", `{}`, wire.CodeInvalid}, {"packages.preview", `{"source":"bad"}`, wire.CodeInvalid},
		{"packages.install", `{"source":" "}`, wire.CodeInvalid}, {"packages.install", `{"source":"bad"}`, wire.CodeInvalid},
		{"packages.remove", `{}`, wire.CodeInvalid}, {"packages.setEnabled", `{"enabled":true}`, wire.CodeInvalid},
	} {
		_, e := call2(t, app, c.cmd, c.args)
		wantCode(t, e, c.code, "")
	}
	if slices.Contains(fp.calls, "install:bad") {
		t.Fatal("预览不过不应安装")
	}
	pv, e := call2(t, app, "packages.preview", `{"source":"ok"}`)
	if e != nil || pv.(wire.PackagesPreviewResult).MCPServers == nil || pv.(wire.PackagesPreviewResult).Risks == nil {
		t.Fatalf("preview 的列表字段应为 []：%+v %v", pv, e)
	}

	// install → 重建
	res, e := call2(t, app, "packages.install", `{"source":"ok"}`)
	if e != nil || !res.(wire.PackagesEditResult).Reloaded || res.(wire.PackagesEditResult).Trashed != "t" || fp.reloads != 1 {
		t.Fatalf("install：%+v %v %d", res, e, fp.reloads)
	}
	// 状态没变：不重建
	res, e = call2(t, app, "packages.setEnabled", `{"name":"same","enabled":true}`)
	if e != nil || res.(wire.PackagesEditResult).Reloaded || fp.reloads != 1 {
		t.Fatalf("没变不应重建：%+v %v", res, e)
	}
	// 重建失败 → Undo
	fp.err = &ReloadReverted{Err: errors.New("坏了")}
	_, e = call2(t, app, "packages.remove", `{"name":"demo"}`)
	wantCode(t, e, wire.CodeInvalid, "")
	if e.Details["reverted"] != true || fp.undone != 1 {
		t.Fatalf("回落应 invalid + reverted 并改回磁盘：%+v undone=%d", e.Details, fp.undone)
	}
	fp.err = errors.New("回不去")
	_, e = call2(t, app, "packages.setEnabled", `{"name":"demo","enabled":false}`)
	wantCode(t, e, wire.CodeInternal, "")
	if fp.undone != 2 {
		t.Fatal("重建失败应改回磁盘")
	}
	fp.err = nil

	// viewer：只能 list
	v := app.NewConn(context.Background(), wire.Principal{ID: "v", Role: wire.RoleViewer})
	defer v.Close()
	if _, err := v.Call(context.Background(), "packages.list", nil); err != nil {
		t.Fatalf("viewer packages.list：%v", err)
	}
	n := len(fp.calls)
	for _, cmd := range []string{"packages.preview", "packages.install", "packages.remove", "packages.setEnabled"} {
		_, err := v.Call(context.Background(), cmd, []byte(`{"source":"ok","name":"demo","enabled":false}`))
		var pe *Error
		if !errors.As(err, &pe) || pe.Code != wire.CodeForbidden {
			t.Fatalf("viewer 不能 %s：%v", cmd, err)
		}
	}
	if len(fp.calls) != n {
		t.Fatalf("viewer 的调用不应到达实现：%v", fp.calls[n:])
	}

	// 回合在跑：not_idle（什么都不动），force 先取消
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
	n = len(fp.calls)
	_, e = call2(t, app, "packages.remove", `{"name":"demo"}`)
	wantCode(t, e, wire.CodeNotIdle, "")
	if len(fp.calls) != n {
		t.Fatal("not_idle 时不应动磁盘")
	}
	if _, e := call2(t, app, "packages.remove", `{"name":"demo","force":true}`); e != nil {
		t.Fatalf("force：%v", e)
	}
}

// T24：插件风险由 effects 推断；wire 的风险常量与 runtime/pkgs 的一致。
func TestPluginRisks(t *testing.T) {
	got := PluginRisks([]string{component.EffectTools, component.EffectNetwork, component.EffectSystemPrompt, component.EffectExec, component.EffectRewriteResult})
	if !slices.Equal(got, []string{wire.RiskExec, wire.RiskNetwork, wire.RiskPrompt}) {
		t.Fatalf("风险：%v", got)
	}
	if rs := PluginRisks([]string{component.EffectTools}); len(rs) != 0 {
		t.Fatalf("只有 tools 不算风险：%v", rs)
	}
	w := []string{wire.RiskExec, wire.RiskNetwork, wire.RiskWrite, wire.RiskConfig, wire.RiskPrompt, wire.RiskCommands}
	if !slices.Equal(w, pkgs.RiskOrder) {
		t.Fatalf("wire.Risk* 与 pkgs.RiskOrder 不一致：%v vs %v", w, pkgs.RiskOrder)
	}
}
