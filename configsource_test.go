package tenon_test

// 基础设施②：ConfigSource 接入——租户层与用户层能覆盖 [use.*] 的 tenant 字段，
// limit:"min" 取最小。经 tenon.WithConfigSource 注入。

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Sevyn17/tenon"
	"github.com/Sevyn17/tenon/tenontest"
)

// memSource 内存版 useconf.Source（测试用）。
type memSource struct {
	tenant map[string]map[string]any // component → values
	user   map[string]map[string]any // component → values
}

func (m memSource) Tenant(ctx context.Context, tenant, component string) (map[string]any, error) {
	return m.tenant[component], nil
}

func (m memSource) User(ctx context.Context, tenant, user, component string) (map[string]any, error) {
	return m.user[component], nil
}

// 用户层能把 [use.budget] 的 tokens_per_day 压到更小（limit:"min" 取最小）。
func TestConfigSourceNarrowsBudget(t *testing.T) {
	dir := t.TempDir()
	dd := filepath.Join(dir, "platform", "worker")
	if err := os.MkdirAll(dd, 0o755); err != nil {
		t.Fatal(err)
	}
	man := "format = \"tenon/v1\"\nid = \"worker\"\ndescription = \"干活\"\n\n[use.budget]\ntokens_per_day = 1000\n"
	if err := os.WriteFile(filepath.Join(dd, "agent.toml"), []byte(man), 0o644); err != nil {
		t.Fatal(err)
	}
	src := memSource{user: map[string]map[string]any{
		"budget": {"tokens_per_day": 10}, // 用户上限更小
	}}
	app := tenontest.New(t,
		tenon.WithAgents(tenon.Dir(dir)),
		tenon.WithConfigSource(src),
		tenontest.Model(tenontest.Reply("ok")))
	// 跑一轮：能装配、能跑即说明 ConfigSource 被接受并参与了使用配置链。
	if _, err := app.Ask(context.Background(), "worker", "hi", nil); err != nil {
		t.Fatalf("带 ConfigSource 应能跑：%v", err)
	}
	// 取严：会话算好的 [use.budget] 应是 10（用户层更小），不是定义里的 1000。
	l, err := app.Internals().DB.ListSessions(context.Background(), 0, 10, true)
	if err != nil || len(l) == 0 {
		t.Fatalf("应有会话：%v", err)
	}
	loaded, err := app.Internals().DB.Load(context.Background(), l[0].ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	uc := string(loaded.Meta.UseConfig)
	if uc == "" {
		t.Fatal("会话应存算好的 use_config")
	}
	// use_config 里 budget.tokens_per_day 应为 10。
	if !containsSub(uc, `"tokens_per_day":10`) {
		t.Fatalf("use_config 应取严为 10：%s", uc)
	}
}

func containsSub(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

// 用户层能把 [use.permission] mode 收窄（tenant 标签允许覆盖；取严由 permission 组件链处理）。
func TestConfigSourceNarrowsPermission(t *testing.T) {
	dir := t.TempDir()
	dd := filepath.Join(dir, "platform", "worker")
	if err := os.MkdirAll(dd, 0o755); err != nil {
		t.Fatal(err)
	}
	man := "format = \"tenon/v1\"\nid = \"worker\"\ndescription = \"干活\"\n\n[use.permission]\nmode = \"full\"\n"
	if err := os.WriteFile(filepath.Join(dd, "agent.toml"), []byte(man), 0o644); err != nil {
		t.Fatal(err)
	}
	src := memSource{user: map[string]map[string]any{
		"permission": {"mode": "read-only"},
	}}
	app := tenontest.New(t,
		tenon.WithAgents(tenon.Dir(dir)),
		tenon.WithConfigSource(src),
		tenontest.Model(tenontest.Reply("ok")))
	if _, err := app.Ask(context.Background(), "worker", "hi", nil); err != nil {
		t.Fatalf("用户层收窄 mode 应被接受：%v", err)
	}
	l, _ := app.Internals().DB.ListSessions(context.Background(), 0, 10, true)
	if len(l) == 0 {
		t.Fatal("应有会话")
	}
	loaded, _ := app.Internals().DB.Load(context.Background(), l[0].ID, nil)
	if !containsSub(string(loaded.Meta.UseConfig), `"mode":"read-only"`) {
		t.Fatalf("use_config 的 permission.mode 应为 read-only：%s", string(loaded.Meta.UseConfig))
	}
}
