package plugin

import "testing"

func TestOverlayEnabledAndConfigMerge(t *testing.T) {
	base, err := ParseProfile(`
mandatory = ["store"]
[[plugin]]
name = "web"
config = { a = 1, b = 2 }
[kernel]
max_steps = 10
`, ".")
	if err != nil {
		t.Fatal(err)
	}
	user, err := ParseProfile(`
[[plugin]]
name = "web"
enabled = true
config = { b = 3 }
[[plugin]]
name = "mcp"
disabled = true
[kernel]
tool_timeout = "30s"
`, ".")
	if err != nil {
		t.Fatal(err)
	}
	if err := base.Overlay(user); err != nil {
		t.Fatal(err)
	}
	var web, mcp *Entry
	for i := range base.Plugins {
		switch base.Plugins[i].Name {
		case "web":
			web = &base.Plugins[i]
		case "mcp":
			mcp = &base.Plugins[i]
		}
	}
	if web == nil || web.Enabled == nil || !*web.Enabled {
		t.Fatalf("web 应被开启: %+v", web)
	}
	if web.Config["a"] != int64(1) || web.Config["b"] != int64(3) {
		t.Fatalf("config 应按键合并: %v", web.Config)
	}
	if mcp == nil || mcp.Enabled == nil || *mcp.Enabled {
		t.Fatalf("disabled 应等同 enabled=false: %+v", mcp)
	}
	if base.Kernel["max_steps"] != int64(10) || base.Kernel["tool_timeout"] != "30s" {
		t.Fatalf("[kernel] 应按键合并: %v", base.Kernel)
	}

	// 后层未写 enabled 时保留前层开关。
	proj, _ := ParseProfile("[[plugin]]\nname = \"web\"\nconfig = { a = 9 }\n", ".")
	if err := base.Overlay(proj); err != nil {
		t.Fatal(err)
	}
	if !*web.Enabled || web.Config["a"] != int64(9) || web.Config["b"] != int64(3) {
		t.Fatalf("未写 enabled 应保留开启: %+v", web)
	}

	off, _ := ParseProfile("[[plugin]]\nname = \"store\"\nenabled = false\n", ".")
	if err := base.Overlay(off); err == nil {
		t.Fatal("关闭强制插件应报错")
	}
}
