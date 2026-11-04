package ybase

import (
	"context"
	"errors"
	"fmt"

	"ybase/runtime/assemble"
	"ybase/runtime/store"
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

// nopUI 缺省界面：丢弃输出，提问直接报错。
type nopUI struct{}

func (nopUI) Print(string) {}

func (nopUI) Warn(string, string) {}

func (nopUI) Ask(ctx context.Context, question string, options []string) (string, error) {
	return "", errors.New("ybase: 没有界面可提问（WithUI 提供）")
}

var _ assemble.UI = nopUI{}
