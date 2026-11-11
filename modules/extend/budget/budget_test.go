package budget_test

// [use.budget] 经定义 → 会话算好 → 记账上限可用（T4f 基础设施）。

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Sevyn17/tenon"
	"github.com/Sevyn17/tenon/tenontest"
)

// 定义里写 [use.budget] 能通过使用配置校验（budget 组件登记了 schema）。
func TestUseBudgetAccepted(t *testing.T) {
	dir := t.TempDir()
	dd := filepath.Join(dir, "platform", "worker")
	if err := os.MkdirAll(dd, 0o755); err != nil {
		t.Fatal(err)
	}
	man := "format = \"tenon/v1\"\nid = \"worker\"\ndescription = \"干活\"\n\n[use.budget]\ntokens_per_day = 1000\n"
	if err := os.WriteFile(filepath.Join(dd, "agent.toml"), []byte(man), 0o644); err != nil {
		t.Fatal(err)
	}
	// 装配成功即说明 [use.budget] 有组件认领、schema 校验通过。
	app := tenontest.New(t, tenon.WithAgents(tenon.Dir(dir)), tenontest.Model(tenontest.Reply("ok")))
	if _, err := app.Ask(context.Background(), "worker", "hi", nil); err != nil {
		t.Fatalf("带 [use.budget] 的定义应能跑：%v", err)
	}
}
