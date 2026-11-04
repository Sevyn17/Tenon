package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ybase/apps/internal/boot"
)

func writePkg(t *testing.T, dir, manifest string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ybase-plugin.toml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
}

// 分层：插件包 [config.*] 低于用户 profile；未受信项目包跳过并提示；内容所属插件没开时提示。
func TestLoadAllPackagesLayering(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YBASE_HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	writePkg(t, filepath.Join(home, "plugins", "demo"), `name = "demo"
[[server]]
name = "x"
command = "echo"
[config.compact-basic]
keep_ratio = 0.3
headroom = 1000
`)
	if err := os.WriteFile(filepath.Join(home, "profile.toml"),
		[]byte("[[plugin]]\nname = \"compact-basic\"\nconfig = { headroom = 2000 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	writePkg(t, filepath.Join(root, ".ybase", "plugins", "proj"), `name = "proj"`)

	p, ps, notes, err := boot.LoadAll("", root, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || ps[0].Name != "demo" {
		t.Fatalf("应只加载用户级包，得到 %v", ps)
	}
	var cfg map[string]any
	for _, e := range p.Plugins {
		if e.Name == "compact-basic" {
			cfg = e.Config
		}
	}
	if cfg["keep_ratio"] != 0.3 {
		t.Fatalf("包配置未生效：%v", cfg)
	}
	if v, _ := cfg["headroom"].(int64); v != 2000 {
		if v2, _ := cfg["headroom"].(int); v2 != 2000 {
			t.Fatalf("用户 profile 应覆盖包配置：%v (%T)", cfg["headroom"], cfg["headroom"])
		}
	}
	all := strings.Join(notes, "\n")
	if !strings.Contains(all, "跳过项目插件包") {
		t.Fatalf("未受信应提示跳过：%s", all)
	}
	if !strings.Contains(all, "MCP 服务需开 mcp") {
		t.Fatalf("mcp 未开应提示：%s", all)
	}

	// 受信：项目包加载，排在用户包之后。
	_, ps, _, err = boot.LoadAll("", root, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 2 || ps[1].Name != "proj" {
		t.Fatalf("受信应加载项目包：%v", ps)
	}

	// 坏清单：错误带文件名。
	writePkg(t, filepath.Join(home, "plugins", "bad"), "name = \"bad\"\nbogus = 1\n")
	if _, _, _, err := boot.LoadAll("", root, false); err == nil || !strings.Contains(err.Error(), "ybase-plugin.toml") {
		t.Fatalf("坏清单应报错并带文件名：%v", err)
	}
}
