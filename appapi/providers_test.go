package appapi_test

// models.providers.*（Mortise docs/51 第 4 节）的契约测试：实现了 ModelProviders 才报能力 providers、命令转发、
// 列表字段总是数组、参数校验、stale 冲突原样交出、viewer 全部 forbidden。

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/Sevyn17/tenon/appapi"
	"github.com/Sevyn17/tenon/appapi/wire"
)

type provPlatform struct {
	fakePlatform
	saved   []wire.ModelProviderDraft
	removed []string
	toggled []wire.ModelProvidersSetModelEnabledArgs
	rev     string
}

func (p *provPlatform) ProvidersList(context.Context) (wire.ModelProvidersListResult, error) {
	return wire.ModelProvidersListResult{Providers: []wire.ModelProviderInfo{{ID: "deepseek", Kind: "deepseek"}}, Revision: p.rev}, nil
}

func (p *provPlatform) ProvidersSave(_ context.Context, d wire.ModelProviderDraft, rev string) (appapi.ProvidersWrite, error) {
	if rev != "" && rev != p.rev {
		return appapi.ProvidersWrite{}, appapi.StaleRevision(p.rev)
	}
	p.saved = append(p.saved, d)
	return appapi.ProvidersWrite{Revision: "r2", Provider: &wire.ModelProviderInfo{ID: d.ID}, Reload: true}, nil
}

func (p *provPlatform) ProvidersRemove(_ context.Context, id, _ string) (appapi.ProvidersWrite, error) {
	p.removed = append(p.removed, id)
	return appapi.ProvidersWrite{Revision: "r3"}, nil
}

func (p *provPlatform) ProvidersSetModelEnabled(_ context.Context, in wire.ModelProvidersSetModelEnabledArgs) (appapi.ProvidersWrite, error) {
	p.toggled = append(p.toggled, in)
	return appapi.ProvidersWrite{Revision: "r4"}, nil
}

func (p *provPlatform) ProvidersTest(context.Context, wire.ModelProvidersTestArgs) (wire.BYOKTestResult, error) {
	return wire.BYOKTestResult{OK: true, Message: "ok"}, nil
}

func (p *provPlatform) ProvidersFetch(context.Context, wire.ModelProvidersFetchArgs) (wire.ModelProvidersFetchResult, error) {
	return wire.ModelProvidersFetchResult{OK: false, ErrorKind: "not_found", Message: "没有列表接口"}, nil
}

func wantErrCode(t *testing.T, err error, code string) *appapi.Error {
	t.Helper()
	var ae *appapi.Error
	if !errors.As(err, &ae) || ae.Code != code {
		t.Fatalf("应为 %s：%v", code, err)
	}
	return ae
}

func TestProvidersCapability(t *testing.T) {
	if slices.Contains(caps(t, newApp(t, &fakePlatform{})), appapi.CapProviders) {
		t.Fatal("没实现 ModelProviders 不该报 providers")
	}
	if !slices.Contains(caps(t, newApp(t, &provPlatform{})), appapi.CapProviders) {
		t.Fatal("实现了应报 providers")
	}
	app := newApp(t, &fakePlatform{})
	_, err := app.ModelProvidersList(context.Background(), wire.Empty{})
	if !isUnsupported(err) {
		t.Fatalf("没实现应 unsupported：%v", err)
	}
}

func TestProvidersDelegates(t *testing.T) {
	pp := &provPlatform{rev: "r1"}
	app := newApp(t, pp)
	ctx := context.Background()

	l, err := app.ModelProvidersList(ctx, wire.Empty{})
	if err != nil || l.Kinds == nil || len(l.Providers) != 1 || l.Providers[0].Keys == nil || l.Providers[0].Models == nil {
		t.Fatalf("列表字段应总是数组：%+v %v", l, err)
	}
	r, err := app.ModelProvidersSave(ctx, wire.ModelProvidersSaveArgs{Provider: wire.ModelProviderDraft{ID: " deepseek "}, Revision: "r1"})
	if err != nil || r.Revision != "r2" || r.Reloaded || r.Provider == nil || r.Provider.Keys == nil {
		t.Fatalf("save（没有工作区管理器不重建）：%+v %v", r, err)
	}
	if len(pp.saved) != 1 || pp.saved[0].ID != "deepseek" {
		t.Fatalf("id 应去空白后转发：%+v", pp.saved)
	}
	_, err = app.ModelProvidersSave(ctx, wire.ModelProvidersSaveArgs{Provider: wire.ModelProviderDraft{ID: "deepseek"}, Revision: "old"})
	ae := wantErrCode(t, err, wire.CodeConflict)
	if ae.Details["reason"] != wire.ConflictStale || ae.Details["revision"] != "r1" {
		t.Fatalf("stale 冲突带当前版本：%+v", ae.Details)
	}
	_, err = app.ModelProvidersSave(ctx, wire.ModelProvidersSaveArgs{})
	wantErrCode(t, err, wire.CodeInvalid)
	if _, err := app.ModelProvidersRemove(ctx, wire.ModelProvidersRemoveArgs{Provider: "x"}); err != nil || !slices.Equal(pp.removed, []string{"x"}) {
		t.Fatalf("remove：%v %v", err, pp.removed)
	}
	_, err = app.ModelProvidersSetModelEnabled(ctx, wire.ModelProvidersSetModelEnabledArgs{Provider: "x"})
	wantErrCode(t, err, wire.CodeInvalid)
	if _, err := app.ModelProvidersSetModelEnabled(ctx, wire.ModelProvidersSetModelEnabledArgs{Provider: "x", Name: "m"}); err != nil || len(pp.toggled) != 1 {
		t.Fatalf("setModelEnabled：%v", err)
	}
	if tr, err := app.ModelProvidersTest(ctx, wire.ModelProvidersTestArgs{Provider: "x"}); err != nil || !tr.OK {
		t.Fatalf("test：%+v %v", tr, err)
	}
	fr, err := app.ModelProvidersFetchModels(ctx, wire.ModelProvidersFetchArgs{Kind: "ollama"})
	if err != nil || fr.OK || fr.Models == nil || fr.ErrorKind != "not_found" {
		t.Fatalf("拉不到不是错误、models 是数组：%+v %v", fr, err)
	}
	_, err = app.ModelProvidersTest(ctx, wire.ModelProvidersTestArgs{})
	wantErrCode(t, err, wire.CodeInvalid)
}

func TestProvidersViewerForbidden(t *testing.T) {
	pp := &provPlatform{}
	app := newApp(t, pp)
	c := app.NewConn(context.Background(), wire.Principal{ID: "v", Role: wire.RoleViewer})
	defer c.Close()
	for _, cmd := range []string{"models.providers.list", "models.providers.save", "models.providers.remove",
		"models.providers.setModelEnabled", "models.providers.test", "models.providers.fetchModels"} {
		_, err := c.Call(context.Background(), cmd, []byte(`{"provider":{"id":"deepseek"}}`))
		wantErrCode(t, err, wire.CodeForbidden)
	}
	if len(pp.saved)+len(pp.removed)+len(pp.toggled) != 0 {
		t.Fatal("viewer 的调用不应到达实现")
	}
}
