package appapi_test

// Platform 的可选扩展（客户端改进 M3：配置T6 测试连接、按 id 编辑，配置T8 保存 server 地址）的契约测试：
// 实现了才报能力、命令转发到实现；没实现时 unsupported，带 id 的保存不会悄悄当成新增。

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/Sevyn17/tenon/appapi"
	"github.com/Sevyn17/tenon/appapi/wire"
)

// extPlatform 在 fakePlatform 之上实现三个可选接口，记下收到的参数。
type extPlatform struct {
	fakePlatform
	tested    []wire.BYOKTestArgs
	updated   []wire.BYOKSaveArgs
	saved     []wire.BYOKSaveArgs
	configure []wire.PlatformConfigureArgs
}

func (e *extPlatform) BYOKSave(_ context.Context, in wire.BYOKSaveArgs) (wire.BYOKSaveResult, error) {
	e.saved = append(e.saved, in)
	return wire.BYOKSaveResult{Profile: wire.BYOKProfile{ID: "new", Name: in.Name}}, nil
}

func (e *extPlatform) BYOKTest(_ context.Context, in wire.BYOKTestArgs) (wire.BYOKTestResult, error) {
	e.tested = append(e.tested, in)
	return wire.BYOKTestResult{OK: true, Method: "models", Message: "连接成功"}, nil
}

func (e *extPlatform) BYOKUpdate(_ context.Context, in wire.BYOKSaveArgs) (wire.BYOKSaveResult, error) {
	e.updated = append(e.updated, in)
	return wire.BYOKSaveResult{Profile: wire.BYOKProfile{ID: in.ID, Name: in.Name}}, nil
}

func (e *extPlatform) Configure(_ context.Context, in wire.PlatformConfigureArgs) (wire.PlatformConfigureResult, error) {
	e.configure = append(e.configure, in)
	return wire.PlatformConfigureResult{Saved: !in.DryRun, Status: wire.AccountStatus{Configured: true, ServerURL: in.ServerURL, ServerSource: "file"}}, nil
}

func caps(t *testing.T, app *appapi.App) []string {
	t.Helper()
	h, err := app.Hello(context.Background(), wire.HelloArgs{})
	if err != nil {
		t.Fatalf("hello: %v", err)
	}
	return h.Capabilities
}

func isUnsupported(err error) bool {
	var ae *appapi.Error
	return errors.As(err, &ae) && ae.Code == wire.CodeUnsupported
}

// TestPlatformExtCapabilities 实现了可选接口才报 byok_test / byok_edit / platform_configure。
func TestPlatformExtCapabilities(t *testing.T) {
	ext := []string{"byok_test", "byok_edit", "platform_configure"}
	for _, c := range ext {
		if got := caps(t, newApp(t, &fakePlatform{})); slices.Contains(got, c) {
			t.Errorf("只实现 Platform 时不该报 %s：%v", c, got)
		}
		if got := caps(t, newApp(t, nil)); slices.Contains(got, c) {
			t.Errorf("没接平台时不该报 %s：%v", c, got)
		}
		if got := caps(t, newApp(t, &extPlatform{})); !slices.Contains(got, c) {
			t.Errorf("实现了可选接口应报 %s：%v", c, got)
		}
	}
}

// TestPlatformExtDelegates 三条路径转发到实现；不带 id 的保存仍走 BYOKSave。
func TestPlatformExtDelegates(t *testing.T) {
	ep := &extPlatform{}
	app := newApp(t, ep)
	ctx := context.Background()

	if r, err := app.PlatformBYOKTest(ctx, wire.BYOKTestArgs{ID: "b1"}); err != nil || !r.OK {
		t.Fatalf("byok.test: %+v %v", r, err)
	}
	if len(ep.tested) != 1 || ep.tested[0].ID != "b1" {
		t.Errorf("byok.test 应转发参数：%+v", ep.tested)
	}

	if _, err := app.PlatformBYOKSave(ctx, wire.BYOKSaveArgs{ID: "b1", Name: "改名"}); err != nil {
		t.Fatalf("byok.save {id}: %v", err)
	}
	if _, err := app.PlatformBYOKSave(ctx, wire.BYOKSaveArgs{Name: "新的"}); err != nil {
		t.Fatalf("byok.save: %v", err)
	}
	if len(ep.updated) != 1 || ep.updated[0].ID != "b1" || len(ep.saved) != 1 || ep.saved[0].Name != "新的" {
		t.Errorf("带 id 走 BYOKUpdate、不带走 BYOKSave：updated=%+v saved=%+v", ep.updated, ep.saved)
	}

	r, err := app.PlatformConfigure(ctx, wire.PlatformConfigureArgs{ServerURL: "http://x", DryRun: true})
	if err != nil || r.Saved || r.Status.ServerSource != "file" {
		t.Fatalf("configure: %+v %v", r, err)
	}
	if len(ep.configure) != 1 || !ep.configure[0].DryRun {
		t.Errorf("configure 应转发参数：%+v", ep.configure)
	}
}

// TestPlatformExtUnsupported 没实现可选接口（或没接平台）时 unsupported；带 id 的保存不落到 BYOKSave。
func TestPlatformExtUnsupported(t *testing.T) {
	ctx := context.Background()
	for name, p := range map[string]appapi.Platform{"nil": nil, "plain": &fakePlatform{}} {
		app := newApp(t, p)
		if _, err := app.PlatformBYOKTest(ctx, wire.BYOKTestArgs{}); !isUnsupported(err) {
			t.Errorf("%s: byok.test 应 unsupported，实际 %v", name, err)
		}
		if _, err := app.PlatformConfigure(ctx, wire.PlatformConfigureArgs{ServerURL: "http://x"}); !isUnsupported(err) {
			t.Errorf("%s: configure 应 unsupported，实际 %v", name, err)
		}
		if _, err := app.PlatformBYOKSave(ctx, wire.BYOKSaveArgs{ID: "b1", Name: "x"}); !isUnsupported(err) {
			t.Errorf("%s: 带 id 的 byok.save 应 unsupported，实际 %v", name, err)
		}
	}
}

// TestPlatformExtCommandsRegistered 新命令在协议命令表里。
func TestPlatformExtCommandsRegistered(t *testing.T) {
	for _, want := range []string{"platform.byok.test", "platform.configure"} {
		if !slices.Contains(appapi.Commands(), want) {
			t.Errorf("命令表缺 %q", want)
		}
	}
}
