package ybase

import (
	"context"
	"errors"

	kr "ybase-kernel/core/runtime"
)

// ctxKey 身份在 context 里的键。
type ctxKey int

const (
	tenantKey ctxKey = iota + 1
	userKey
)

// WithTenant 在 context 上带租户（单机恒为 local，SDK 保留多租户入口，07 7.1）。
func WithTenant(ctx context.Context, tenant string) context.Context {
	return context.WithValue(ctx, tenantKey, tenant)
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

var _ kr.UI = nopUI{}
