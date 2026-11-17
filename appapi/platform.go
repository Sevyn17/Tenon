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

	// BYOKList 列出用户自己的模型配置（脱敏，不含明文 key）。
	//
	// **为什么在框架接口里**：BYOK 是「用户自带厂商凭据」的通用形态，
	// 与具体平台无关——纯本地用户也需要多套配置。
	BYOKList(ctx context.Context) (wire.BYOKListResult, error)
	// BYOKSave 新增或更新一套配置（同名视为更新）。
	BYOKSave(ctx context.Context, in wire.BYOKSaveArgs) (wire.BYOKSaveResult, error)
	// BYOKRemove 删除一套配置。
	BYOKRemove(ctx context.Context, in wire.BYOKRemoveArgs) (wire.BYOKRemoveResult, error)
	// BYOKSetDefault 设为默认。
	BYOKSetDefault(ctx context.Context, in wire.BYOKSetDefaultArgs) (wire.BYOKSetDefaultResult, error)

	// PasswordRegister 用邮箱 + 密码注册（并自动登录）。
	//
	// **与设备码登录并存**：设备码适合首次（没有账号），密码适合日常。
	PasswordRegister(ctx context.Context, in wire.PasswordAuthArgs) (wire.AccountStatus, error)
	// PasswordLogin 用邮箱 + 密码登录。
	PasswordLogin(ctx context.Context, in wire.PasswordAuthArgs) (wire.AccountStatus, error)

	// AdminModels 拉平台侧的模型目录（**只读**，需要 operator 令牌）。
	//
	// **为什么在框架接口里**：任何「有运营后台的产品」都需要「看平台侧配了什么」。
	// 与 BYOK 的区别是受众与权限：BYOK 是用户自己的，这个是平台的。
	AdminModels(ctx context.Context) (wire.AdminModelsResult, error)
}

// —— Platform 的可选扩展（客户端改进 M3）——
//
// 为什么是可选接口而不是往 Platform 里加方法：加方法会让所有既有实现（含测试替身）编译不过；
// 这与 Reloader 之于 Workspaces 是同一个模式。实现了才在 hello.capabilities 里报对应能力，界面据此显示入口。

// BYOKTester 测一套 BYOK 配置能不能连通（platform.byok.test，能力 byok_test）。
// 连不通不是错误：返回 OK=false 的结果；只有内部故障（读不了配置文件等）才返回 error。
// 实现方保证结果、错误、日志里都没有明文 Key。
type BYOKTester interface {
	BYOKTest(ctx context.Context, in wire.BYOKTestArgs) (wire.BYOKTestResult, error)
}

// BYOKEditor 按 id 编辑已有配置（platform.byok.save 带 id，能力 byok_edit）：APIKey 留空表示不改，
// 名称只和其他配置比重名。没实现时带 id 的保存返回 unsupported（不悄悄当成新增，免得多出一套）。
type BYOKEditor interface {
	BYOKUpdate(ctx context.Context, in wire.BYOKSaveArgs) (wire.BYOKSaveResult, error)
}

// PlatformConfigurer 保存平台 server 地址（platform.configure，能力 platform_configure）。
type PlatformConfigurer interface {
	Configure(ctx context.Context, in wire.PlatformConfigureArgs) (wire.PlatformConfigureResult, error)
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

// —— BYOK（docs/37 批次 B）——

// PlatformBYOKList 列出 BYOK 配置。
func (v *App) PlatformBYOKList(ctx context.Context, _ wire.Empty) (wire.BYOKListResult, error) {
	if v.o.Platform == nil {
		return wire.BYOKListResult{}, nil
	}
	return v.o.Platform.BYOKList(ctx)
}

// PlatformBYOKSave 保存 BYOK 配置。
func (v *App) PlatformBYOKSave(ctx context.Context, in wire.BYOKSaveArgs) (wire.BYOKSaveResult, error) {
	if v.o.Platform == nil {
		return wire.BYOKSaveResult{}, Errorf(CodeUnsupported, "这个应用没有接入模型配置")
	}
	if in.ID != "" {
		ed, ok := v.o.Platform.(BYOKEditor)
		if !ok {
			return wire.BYOKSaveResult{}, Errorf(CodeUnsupported, "这个应用不支持按 id 编辑模型配置")
		}
		return ed.BYOKUpdate(ctx, in)
	}
	return v.o.Platform.BYOKSave(ctx, in)
}

// PlatformBYOKTest 测一套 BYOK 配置能不能连通（客户端改进 M3 配置T6）。
func (v *App) PlatformBYOKTest(ctx context.Context, in wire.BYOKTestArgs) (wire.BYOKTestResult, error) {
	t, ok := v.o.Platform.(BYOKTester)
	if v.o.Platform == nil || !ok {
		return wire.BYOKTestResult{}, Errorf(CodeUnsupported, "这个应用不支持测试模型连接")
	}
	return t.BYOKTest(ctx, in)
}

// PlatformConfigure 保存平台 server 地址（客户端改进 M3 配置T8）。
func (v *App) PlatformConfigure(ctx context.Context, in wire.PlatformConfigureArgs) (wire.PlatformConfigureResult, error) {
	c, ok := v.o.Platform.(PlatformConfigurer)
	if v.o.Platform == nil || !ok {
		return wire.PlatformConfigureResult{}, Errorf(CodeUnsupported, "这个应用不支持在界面上设置平台地址")
	}
	return c.Configure(ctx, in)
}

// PlatformBYOKRemove 删除 BYOK 配置。
func (v *App) PlatformBYOKRemove(ctx context.Context, in wire.BYOKRemoveArgs) (wire.BYOKRemoveResult, error) {
	if v.o.Platform == nil {
		return wire.BYOKRemoveResult{}, Errorf(CodeUnsupported, "这个应用没有接入模型配置")
	}
	return v.o.Platform.BYOKRemove(ctx, in)
}

// PlatformBYOKSetDefault 设为默认。
func (v *App) PlatformBYOKSetDefault(ctx context.Context, in wire.BYOKSetDefaultArgs) (wire.BYOKSetDefaultResult, error) {
	if v.o.Platform == nil {
		return wire.BYOKSetDefaultResult{}, Errorf(CodeUnsupported, "这个应用没有接入模型配置")
	}
	return v.o.Platform.BYOKSetDefault(ctx, in)
}

// PlatformAdminModels 拉模型目录（只读）。
func (v *App) PlatformAdminModels(ctx context.Context, _ wire.Empty) (wire.AdminModelsResult, error) {
	if v.o.Platform == nil {
		return wire.AdminModelsResult{}, Errorf(CodeUnsupported, "这个应用没有接入模型目录")
	}
	return v.o.Platform.AdminModels(ctx)
}

// PlatformPasswordRegister 邮箱 + 密码注册。
func (v *App) PlatformPasswordRegister(ctx context.Context, in wire.PasswordAuthArgs) (wire.AccountStatus, error) {
	if v.o.Platform == nil {
		return wire.AccountStatus{}, Errorf(CodeUnsupported, "这个应用没有接入平台账号")
	}
	return v.o.Platform.PasswordRegister(ctx, in)
}

// PlatformPasswordLogin 邮箱 + 密码登录。
func (v *App) PlatformPasswordLogin(ctx context.Context, in wire.PasswordAuthArgs) (wire.AccountStatus, error) {
	if v.o.Platform == nil {
		return wire.AccountStatus{}, Errorf(CodeUnsupported, "这个应用没有接入平台账号")
	}
	return v.o.Platform.PasswordLogin(ctx, in)
}
