package appapi_test

// 平台账号命令的契约测试。
//
// 核心是 **nil 语义**：应用没接平台时，status / logout / entitlements 要返回
// `Configured=false` 的**正常结果**，而不是错误——否则纯本地用户会看到无意义的报错。
// 只有 login 在未接平台时返回 unsupported（因为那时点「登录」确实没有东西可做）。
//
// 这与既有可选能力（Modes / Workspaces / Compactor）的口径一致。

import (
	"context"
	"errors"
	"testing"

	"github.com/Sevyn17/tenon/appapi"
	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/runtime/bus"
	"github.com/Sevyn17/tenon/runtime/host"
	"github.com/Sevyn17/tenon/runtime/store/memstore"
)

// newApp 造一个最小可用 App（不需要完整装配）。
//
// 平台命令不碰 Host 的业务逻辑，但 appapi.New 要求 Host 非 nil（它是会话构造器），
// 所以给一个最小的 Host。
func newApp(t *testing.T, p appapi.Platform) *appapi.App {
	t.Helper()
	db := memstore.New()
	b := bus.New(0)
	h := &host.Host{DB: db, Bus: b}
	t.Cleanup(func() { h.Close(); b.Close(); db.Close() })
	app, err := appapi.New(appapi.Options{
		Host:     h,
		DB:       db,
		Bus:      b,
		Workdir:  t.TempDir(),
		Platform: p,
	})
	if err != nil {
		t.Fatalf("appapi.New: %v", err)
	}
	return app
}

// fakePlatform 记录调用，返回固定结果。
type fakePlatform struct {
	statusCalls int
	loginCalls  []wire.AccountLoginArgs
	logoutCalls int
	entCalls    int
	loginErr    error
}

// API Key（2026-11-16 扩展 Platform 接口后补的 mock）。
func (f *fakePlatform) APIKeyList(context.Context) (wire.APIKeyListResult, error) {
	return wire.APIKeyListResult{Configured: true, Keys: []wire.APIKey{
		{ID: "k1", Name: "测试", Prefix: "sk-abcd1234", CreatedAt: "2026-11-16T00:00:00Z"},
	}}, nil
}

func (f *fakePlatform) APIKeyCreate(_ context.Context, in wire.APIKeyCreateArgs) (wire.APIKeyCreateResult, error) {
	return wire.APIKeyCreateResult{ID: "k2", Name: in.Name, Prefix: "sk-newkey12",
		CreatedAt: "2026-11-16T00:00:00Z", Key: "sk-newkey123456"}, nil
}

func (f *fakePlatform) APIKeyRevoke(context.Context, wire.APIKeyRevokeArgs) (wire.APIKeyRevokeResult, error) {
	return wire.APIKeyRevokeResult{}, nil
}

// BYOK（2026-11-16 扩展 Platform 接口后补的 mock）。
func (f *fakePlatform) BYOKList(context.Context) (wire.BYOKListResult, error) {
	return wire.BYOKListResult{Profiles: []wire.BYOKProfile{
		{ID: "b1", Name: "测试配置", Protocol: "openai",
			BaseURL: "https://api.example.com/v1", Model: "gpt-x",
			MaskedKey: "sk-t…7890", IsDefault: true},
	}, Path: "/tmp/byok.json"}, nil
}

func (f *fakePlatform) BYOKSave(_ context.Context, in wire.BYOKSaveArgs) (wire.BYOKSaveResult, error) {
	return wire.BYOKSaveResult{Profile: wire.BYOKProfile{
		ID: "b2", Name: in.Name, Protocol: in.Protocol,
		BaseURL: in.BaseURL, Model: in.Model, MaskedKey: "sk-n…1234",
	}}, nil
}

func (f *fakePlatform) BYOKRemove(context.Context, wire.BYOKRemoveArgs) (wire.BYOKRemoveResult, error) {
	return wire.BYOKRemoveResult{}, nil
}

func (f *fakePlatform) BYOKSetDefault(context.Context, wire.BYOKSetDefaultArgs) (wire.BYOKSetDefaultResult, error) {
	return wire.BYOKSetDefaultResult{}, nil
}

// AdminModels（2026-11-16 扩展 Platform 接口后补的 mock）。
func (f *fakePlatform) AdminModels(context.Context) (wire.AdminModelsResult, error) {
	return wire.AdminModelsResult{
		Providers: []wire.AdminModelProvider{
			{ID: "p1", Key: "fake", Name: "假厂商", Protocol: "openai-chat",
				BaseURL: "http://x", Status: "active", KeyCount: 2},
		},
		Aliases:  []wire.AdminAlias{{Alias: "fake-chat", Kind: "chat", Visible: true, StepCount: 1}},
		ReadOnly: true,
	}, nil
}
func (f *fakePlatform) Status(context.Context) (wire.AccountStatus, error) {
	f.statusCalls++
	return wire.AccountStatus{Configured: true, ServerURL: "http://x", LoggedIn: true, Tenant: "t1"}, nil
}

func (f *fakePlatform) Login(_ context.Context, in wire.AccountLoginArgs) (wire.AccountLoginResult, error) {
	f.loginCalls = append(f.loginCalls, in)
	if f.loginErr != nil {
		return wire.AccountLoginResult{}, f.loginErr
	}
	if in.Step == "start" {
		return wire.AccountLoginResult{
			UserCode: "ABCD-1234", VerificationURI: "http://x/device",
			ExpiresIn: 600, Interval: 5, DeviceCode: "dc-1",
		}, nil
	}
	return wire.AccountLoginResult{Done: true, Status: &wire.AccountStatus{Configured: true, LoggedIn: true}}, nil
}

func (f *fakePlatform) Logout(context.Context) (wire.AccountStatus, error) {
	f.logoutCalls++
	return wire.AccountStatus{Configured: true, LoggedIn: false}, nil
}

func (f *fakePlatform) Entitlements(context.Context) (wire.AccountEntitlements, error) {
	f.entCalls++
	return wire.AccountEntitlements{Configured: true, Features: map[string]bool{"mcp": true}}, nil
}

// TestPlatformNilStatusIsNotError 未接平台时 status 返回 Configured=false，不是错误。
func TestPlatformNilStatusIsNotError(t *testing.T) {
	app := newApp(t, nil)
	st, err := app.PlatformStatus(context.Background(), wire.Empty{})
	if err != nil {
		t.Fatalf("未接平台不该报错：%v", err)
	}
	if st.Configured {
		t.Error("Configured 应为 false")
	}
}

// TestPlatformNilLogoutIsNotError 未接平台时 logout 也不报错（幂等语义）。
func TestPlatformNilLogoutIsNotError(t *testing.T) {
	app := newApp(t, nil)
	st, err := app.PlatformLogout(context.Background(), wire.Empty{})
	if err != nil {
		t.Fatalf("未接平台不该报错：%v", err)
	}
	if st.Configured {
		t.Error("Configured 应为 false")
	}
}

// TestPlatformNilEntitlementsIsNotError 未接平台时权益返回 Configured=false。
func TestPlatformNilEntitlementsIsNotError(t *testing.T) {
	app := newApp(t, nil)
	ent, err := app.PlatformEntitlements(context.Background(), wire.Empty{})
	if err != nil {
		t.Fatalf("未接平台不该报错：%v", err)
	}
	if ent.Configured {
		t.Error("Configured 应为 false")
	}
}

// TestPlatformNilLoginIsUnsupported 未接平台时登录返回 unsupported（确实没东西可做）。
func TestPlatformNilLoginIsUnsupported(t *testing.T) {
	app := newApp(t, nil)
	_, err := app.PlatformLogin(context.Background(), wire.AccountLoginArgs{Step: "start"})
	if err == nil {
		t.Fatal("未接平台的登录应报错")
	}
	var ae *appapi.Error
	if !errors.As(err, &ae) || ae.Code != wire.CodeUnsupported {
		t.Errorf("应是 unsupported，实际 %v", err)
	}
}

// TestPlatformLoginSteps start/poll 两步都要能走通，非法 step 拒绝。
func TestPlatformLoginSteps(t *testing.T) {
	fp := &fakePlatform{}
	app := newApp(t, fp)

	start, err := app.PlatformLogin(context.Background(), wire.AccountLoginArgs{Step: "start"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if start.UserCode == "" || start.VerificationURI == "" || start.DeviceCode == "" {
		t.Errorf("start 应返回 user_code / verification_uri / device_code：%+v", start)
	}

	poll, err := app.PlatformLogin(context.Background(), wire.AccountLoginArgs{Step: "poll", DeviceCode: start.DeviceCode})
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if !poll.Done {
		t.Error("poll 成功应 Done=true")
	}
	if poll.Status == nil || !poll.Status.LoggedIn {
		t.Errorf("poll 应带回登录后的状态：%+v", poll.Status)
	}
	if len(fp.loginCalls) != 2 {
		t.Errorf("应调用两次，实际 %d", len(fp.loginCalls))
	}

	// 非法 step
	_, err = app.PlatformLogin(context.Background(), wire.AccountLoginArgs{Step: "bogus"})
	if err == nil {
		t.Error("非法 step 应报错")
	}
	var ae *appapi.Error
	if !errors.As(err, &ae) || ae.Code != wire.CodeBadRequest {
		t.Errorf("应是 bad_request，实际 %v", err)
	}
}

// TestPlatformDelegates 接了平台时命令确实转发到实现。
func TestPlatformDelegates(t *testing.T) {
	fp := &fakePlatform{}
	app := newApp(t, fp)

	st, err := app.PlatformStatus(context.Background(), wire.Empty{})
	if err != nil || !st.Configured || st.Tenant != "t1" {
		t.Errorf("status 应转发：%+v %v", st, err)
	}
	if _, err := app.PlatformEntitlements(context.Background(), wire.Empty{}); err != nil {
		t.Errorf("entitlements: %v", err)
	}
	if _, err := app.PlatformLogout(context.Background(), wire.Empty{}); err != nil {
		t.Errorf("logout: %v", err)
	}
	if fp.statusCalls != 1 || fp.entCalls != 1 || fp.logoutCalls != 1 {
		t.Errorf("转发次数不对：%d/%d/%d", fp.statusCalls, fp.entCalls, fp.logoutCalls)
	}
}

// TestPlatformCommandsRegistered 四条命令都在协议命令表里。
func TestPlatformCommandsRegistered(t *testing.T) {
	have := map[string]bool{}
	for _, c := range appapi.Commands() {
		have[c] = true
	}
	for _, want := range []string{"platform.status", "platform.login", "platform.logout", "platform.entitlements"} {
		if !have[want] {
			t.Errorf("命令表缺 %q", want)
		}
	}
}
