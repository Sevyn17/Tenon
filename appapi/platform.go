package appapi

// 平台账号（protocol 2 的可选能力）。
//
// 为什么在框架而不是产品：任何「有云端的产品」都需要「配了平台 → 能登录 → 能看权益」
// 这一套。它不依赖任何具体平台，也不按入口分支——接口由应用注入，nil 表示没接平台。
// 这与 Modes / Workspaces / Compactor 等既有可选能力是同一个模式。
//
// **nil 不是错误**：应用没接平台时，这些命令返回 `Configured=false` 的正常结果，
// 界面据此隐藏登录入口。返回错误会让「纯本地用户」看到无意义的报错。

import (
	"context"

	"github.com/Sevyn17/tenon/appapi/wire"
)

// Platform 平台账号能力。由应用实现并注入；nil = 这个应用不接平台。
//
// 四个方法对应四条命令，语义见各命令的注释。
type Platform interface {
	// Status 当前账号状态（含「是否配置了平台」）。
	Status(ctx context.Context) (wire.AccountStatus, error)
	// Login 推进设备码登录一步（step = start / poll）。
	Login(ctx context.Context, in wire.AccountLoginArgs) (wire.AccountLoginResult, error)
	// Logout 清除本地登录态。
	Logout(ctx context.Context) (wire.AccountStatus, error)
	// Entitlements 取权益摘要（离线时应用可返回缓存并置 Cached）。
	Entitlements(ctx context.Context) (wire.AccountEntitlements, error)

	// APIKeyList 列出当前账号的 API 密钥（不含明文）。
	//
	// **为什么在框架接口里**：任何接平台的产品都需要「用户能生成凭据给脚本用」。
	// 这是平台能力的通用形态，不依赖具体平台。
	APIKeyList(ctx context.Context) (wire.APIKeyListResult, error)
	// APIKeyCreate 签发一把新密钥（明文只返回一次）。
	APIKeyCreate(ctx context.Context, in wire.APIKeyCreateArgs) (wire.APIKeyCreateResult, error)
	// APIKeyRevoke 撤销一把密钥（幂等）。
	APIKeyRevoke(ctx context.Context, in wire.APIKeyRevokeArgs) (wire.APIKeyRevokeResult, error)
}

// PlatformStatus 返回账号状态。
func (v *App) PlatformStatus(ctx context.Context, _ wire.Empty) (wire.AccountStatus, error) {
	if v.o.Platform == nil {
		return wire.AccountStatus{Configured: false}, nil
	}
	return v.o.Platform.Status(ctx)
}

// PlatformLogin 推进登录。
//
// 两步走（设备码）：start 返回 user_code 与验证地址，界面展示给用户；
// 用户确认后 poll 换令牌。界面不用自己维护状态机。
func (v *App) PlatformLogin(ctx context.Context, in wire.AccountLoginArgs) (wire.AccountLoginResult, error) {
	if v.o.Platform == nil {
		return wire.AccountLoginResult{}, Errorf(CodeUnsupported, "这个应用没有接入平台账号")
	}
	switch in.Step {
	case "start", "poll":
	default:
		return wire.AccountLoginResult{}, Errorf(CodeBadRequest, "step 只能是 start 或 poll")
	}
	return v.o.Platform.Login(ctx, in)
}

// PlatformLogout 退出登录。
func (v *App) PlatformLogout(ctx context.Context, _ wire.Empty) (wire.AccountStatus, error) {
	if v.o.Platform == nil {
		return wire.AccountStatus{Configured: false}, nil
	}
	return v.o.Platform.Logout(ctx)
}

// PlatformEntitlements 取权益摘要。
func (v *App) PlatformEntitlements(ctx context.Context, _ wire.Empty) (wire.AccountEntitlements, error) {
	if v.o.Platform == nil {
		return wire.AccountEntitlements{Configured: false}, nil
	}
	return v.o.Platform.Entitlements(ctx)
}

// PlatformAPIKeyList 列出 API 密钥。
func (v *App) PlatformAPIKeyList(ctx context.Context, _ wire.Empty) (wire.APIKeyListResult, error) {
	if v.o.Platform == nil {
		return wire.APIKeyListResult{Configured: false}, nil
	}
	return v.o.Platform.APIKeyList(ctx)
}

// PlatformAPIKeyCreate 签发 API 密钥。
func (v *App) PlatformAPIKeyCreate(ctx context.Context, in wire.APIKeyCreateArgs) (wire.APIKeyCreateResult, error) {
	if v.o.Platform == nil {
		return wire.APIKeyCreateResult{}, Errorf(CodeUnsupported, "这个应用没有接入平台账号")
	}
	return v.o.Platform.APIKeyCreate(ctx, in)
}

// PlatformAPIKeyRevoke 撤销 API 密钥。
func (v *App) PlatformAPIKeyRevoke(ctx context.Context, in wire.APIKeyRevokeArgs) (wire.APIKeyRevokeResult, error) {
	if v.o.Platform == nil {
		return wire.APIKeyRevokeResult{}, Errorf(CodeUnsupported, "这个应用没有接入平台账号")
	}
	return v.o.Platform.APIKeyRevoke(ctx, in)
}
