package tenon_test

// T5a：常驻定义经 SDK 装配可用（不再报未支持），且 Host.Definitions 接上定义来源。

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Sevyn17/tenon"
	"github.com/Sevyn17/tenon/tenontest"
)

func TestResidentDefinitionLoads(t *testing.T) {
	dir := t.TempDir()
	// Dir 布局：platform/<名字>/agent.toml
	dd := filepath.Join(dir, "platform", "resident-bot")
	if err := os.MkdirAll(dd, 0o755); err != nil {
		t.Fatal(err)
	}
	man := "format = \"tenon/v1\"\nid = \"resident-bot\"\nresident = true\ndescription = \"常驻机器人\"\n"
	if err := os.WriteFile(filepath.Join(dd, "agent.toml"), []byte(man), 0o644); err != nil {
		t.Fatal(err)
	}
	// 带常驻定义的应用应能装配（不再报 resident 未支持）。
	app := tenontest.New(t, tenon.WithAgents(tenon.Dir(dir)), tenontest.Model(tenontest.Reply("在")))
	if app == nil {
		t.Fatal("带常驻定义应能装配")
	}
}
