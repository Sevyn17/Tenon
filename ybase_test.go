package ybase_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ybase"
	"ybase/runtime/store/memstore"
	"ybase/ybasetest"
)

// base 最小可装配选项：内存存储 + 脚本模型 + 临时工作目录。
func base(t *testing.T) []ybase.Option {
	t.Helper()
	return []ybase.Option{
		ybase.Minimal(),
		ybase.WithStore(memstore.New()),
		ybase.WithWorkdir(t.TempDir()),
		ybasetest.Model(ybasetest.Reply("ok")),
	}
}

func TestNewNeedsStore(t *testing.T) {
	_, err := ybase.New(ybase.Default())
	if !errors.Is(err, ybase.ErrInvalid) || !strings.Contains(err.Error(), "WithStore") {
		t.Fatalf("err = %v，要 ErrInvalid + WithStore", err)
	}
}

func TestNewNeedsComponents(t *testing.T) {
	_, err := ybase.New(ybase.WithStore(memstore.New()))
	if !errors.Is(err, ybase.ErrInvalid) {
		t.Fatalf("err = %v，要 ErrInvalid", err)
	}
}

func TestWithoutUnknown(t *testing.T) {
	opts := append(base(t), ybase.Without("nope"))
	_, err := ybase.New(opts...)
	if !errors.Is(err, ybase.ErrInvalid) || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("err = %v，要 ErrInvalid + nope", err)
	}
}

func TestDuplicatePreset(t *testing.T) {
	opts := append(base(t), ybase.Default())
	_, err := ybase.New(opts...)
	if !errors.Is(err, ybase.ErrInvalid) || !strings.Contains(err.Error(), "重复") {
		t.Fatalf("err = %v，要 ErrInvalid + 重复", err)
	}
}

func TestAskBeforeStart(t *testing.T) {
	app, err := ybase.New(base(t)...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := app.Ask(context.Background(), "support", "hi", nil); !errors.Is(err, ybase.ErrNotStarted) {
		t.Fatalf("err = %v，要 ErrNotStarted", err)
	}
	if _, err := app.History(context.Background(), "x"); !errors.Is(err, ybase.ErrNotStarted) {
		t.Fatalf("History err = %v，要 ErrNotStarted", err)
	}
}

func TestNewValidatesUseSection(t *testing.T) {
	dir := t.TempDir()
	writeAgent(t, dir, "support", `format = "ybase/v1"
id = "support"
[use.ghost]
foo = "bar"
`)
	opts := append(base(t), ybase.WithAgents(ybase.Dir(dir)))
	_, err := ybase.New(opts...)
	if err == nil || !strings.Contains(err.Error(), "use.ghost") {
		t.Fatalf("err = %v，要报 [use.ghost] 未知组件", err)
	}
}

func TestNewAcceptsPlainDefinition(t *testing.T) {
	dir := t.TempDir()
	writeAgent(t, dir, "support", `format = "ybase/v1"
id = "support"
description = "客服"
`)
	opts := append(base(t), ybase.WithAgents(ybase.Dir(dir)))
	if _, err := ybase.New(opts...); err != nil {
		t.Fatalf("New: %v", err)
	}
}

// Minimal 预设（不含权限组件）能跑带定义的会话：内核补缺省授权层（审查 P0-7：原先 Ask 报
// "装配必须恰好有一个权限策略层"）；定义里的 prompt 也进了会话的系统段（P0-2）。
func TestMinimalAsk(t *testing.T) {
	dir := t.TempDir()
	writeAgent(t, dir, "support", `format = "ybase/v1"
id = "support"
prompt = "你是售后客服"
`)
	opts := append(base(t), ybase.WithAgents(ybase.Dir(dir)))
	app, err := ybase.New(opts...)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer app.Close(ctx)
	r, err := app.Ask(ctx, "support", "hi", nil)
	if err != nil {
		t.Fatalf("Minimal 预设应能跑会话: %v", err)
	}
	if r.Text != "ok" {
		t.Fatalf("回复 = %q", r.Text)
	}
}

// writeAgent 在 <root>/platform/<name>/agent.toml 写一个定义。
func writeAgent(t *testing.T, root, name, manifest string) {
	t.Helper()
	d := filepath.Join(root, "platform", name)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "agent.toml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
}
