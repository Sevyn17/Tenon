package ybase_test

import (
	"context"
	"errors"
	"testing"

	"ybase"
)

// 内核错误包成 SDK 的哨兵，errors.Is 可判（2026-11-04 审查 P1-52：原先 History 查不到会话返回 store.ErrNotFound，
// ybase.ErrNotFound 判不出来）。
func TestHistoryUnknownIsNotFound(t *testing.T) {
	app, err := ybase.New(base(t)...)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	defer app.Close(ctx)
	if err := app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := app.History(ctx, "no-such"); !errors.Is(err, ybase.ErrNotFound) {
		t.Fatalf("不存在的会话应 ErrNotFound：%v", err)
	}
}

// WithTenant 不再是死 API：单机版只有一个租户，带别的租户报 ErrInvalid，带 local 照常。
func TestWithTenant(t *testing.T) {
	app, err := ybase.New(base(t)...)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	defer app.Close(ctx)
	if err := app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Ask(ybase.WithTenant(ctx, "t1"), "nope", "hi", nil); !errors.Is(err, ybase.ErrInvalid) {
		t.Fatalf("单机版带别的租户应 ErrInvalid：%v", err)
	}
	if _, err := app.History(ybase.WithTenant(ctx, "t1"), "x"); !errors.Is(err, ybase.ErrInvalid) {
		t.Fatalf("History 带别的租户应 ErrInvalid：%v", err)
	}
	if _, err := app.Ask(ybase.WithTenant(ctx, ybase.LocalTenant), "nope", "hi", nil); !errors.Is(err, ybase.ErrNotFound) {
		t.Fatalf("租户 local 应照常走到定义查找：%v", err)
	}
}
