package tenon

import (
	"context"
	"errors"
	"fmt"

	"github.com/Sevyn17/tenon/runtime/appid"
	"github.com/Sevyn17/tenon/runtime/assemble"
	"github.com/Sevyn17/tenon/runtime/store"
)

// ctxKey 身份在 context 里的键。
type ctxKey int

const (
	tenantKey ctxKey = iota + 1
	userKey
)

// LocalTenant 单机版唯一的租户。
const LocalTenant = store.Tenant

// WithTenant 在 context 上带租户（07 7.1）。单机版只有 LocalTenant：带别的租户的调用报 ErrInvalid，
// 不会悄悄落到 local 下（2026-11-04 审查 P1-52：原先写进 context 后没有任何地方读，是死 API）。
func WithTenant(ctx context.Context, tenant string) context.Context {
	return context.WithValue(ctx, tenantKey, tenant)
}

// checkTenant 校验 context 上的租户：没带 = LocalTenant。
func checkTenant(ctx context.Context) error {
	if t, ok := ctx.Value(tenantKey).(string); ok && t != LocalTenant {
		return fmt.Errorf("%w: 租户 %q（单机版只有 %q）", ErrInvalid, t, LocalTenant)
	}
	return nil
}

// WithUser 在 context 上带用户 id（写进会话行 creator；空 = 本机）。
func WithUser(ctx context.Context, user string) context.Context {
	return context.WithValue(ctx, userKey, user)
}

// userFrom 取 context 上的用户 id。
func userFrom(ctx context.Context) string {
	v, _ := ctx.Value(userKey).(string)
	return v
}

// AppID 应用身份：程序名、数据目录名、环境变量前缀。零值字段取缺省（tenon / .tenon / TENON）。
//
// 框架按它取用户级目录（$<Env>_HOME，缺省 ~/<Dir>）、项目目录（<工作目录>/<Dir>）、
// 环境变量名（<Env>_API_KEY 等）和提示里的命令名（"<Name> trust"）。
// 插件包清单 tenon-plugin.toml、${TENON_PLUGIN_DIR}、TENON_HOOK_EVENT / TENON_SESSION_ID 不随它变：
// 那是框架和第三方插件、脚本之间的协议。
type AppID = appid.ID

// SetAppID 设置本进程的应用身份。进程级设置，在 New / apphost.Start 之前调一次。
func SetAppID(id AppID) { appid.Set(id) }

// CurrentAppID 当前应用身份（没设过时为缺省）。
func CurrentAppID() AppID { return appid.Get() }

// nopUI 缺省界面：丢弃输出，提问直接报错。
type nopUI struct{}

func (nopUI) Print(string) {}

func (nopUI) Warn(string, string) {}

func (nopUI) Ask(ctx context.Context, question string, options []string) (string, error) {
	return "", errors.New("tenon: 没有界面可提问（WithUI 提供）")
}

var _ assemble.UI = nopUI{}
