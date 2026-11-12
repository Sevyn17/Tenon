package profile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/runtime/component"
)

func TestLoadIncludeMandatoryUnion(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("base.toml", `
mandatory = ["permission"]
[[plugin]]
name = "permission"
[[plugin]]
name = "compact"
config = { threshold = 0.8 }
`)
	child := write("child.toml", `
include = ["base.toml"]
mandatory = ["redact"]
[[plugin]]
name = "redact"
[[plugin]]
name = "compact"
config = { threshold = 0.9 }
`)
	p, err := Load(child)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(p.Mandatory, ",") != "permission,redact" {
		t.Fatalf("mandatory 应取并集: %v", p.Mandatory)
	}
	if len(p.Plugins) != 3 || p.Plugins[1].Config["threshold"] != 0.9 {
		t.Fatalf("后写覆盖整行: %+v", p.Plugins)
	}
	if !strings.HasSuffix(p.Sources["compact"], "child.toml") || !strings.HasSuffix(p.Sources["permission"], "base.toml") {
		t.Fatalf("来源 %v", p.Sources)
	}
	bad := write("bad.toml", `
include = ["base.toml"]
[[plugin]]
name = "permission"
disabled = true
`)
	if _, err := Load(bad); err == nil {
		t.Fatal("子 profile 不能停用父 profile 的强制插件")
	}
	loop := write("loop.toml", `include = ["loop.toml"]`)
	if _, err := Load(loop); err == nil {
		t.Fatal("include 循环应报错")
	}
}

func TestEdit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profile.toml")
	src := `# 注释
include = ["base.toml"]
mandatory = ["store-sqlite"]

[kernel]
max_tool_calls_per_step = 16

[[plugin]]
name = "web"
enabled = true

[[plugin]]
name = "compact-basic"
disabled = true
[plugin.config]
headroom = 100
`
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "base.toml"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	rev, err := Revision(path)
	if err != nil || rev == "" {
		t.Fatal(rev, err)
	}
	on := true
	stale := "x"
	if _, err := Edit(path, component.PluginEdit{Name: "compact-basic", Enabled: &on}, &stale); !errors.Is(err, component.ErrRevision) {
		t.Fatalf("revision 不符应 ErrRevision：%v", err)
	}
	rev2, err := Edit(path, component.PluginEdit{Name: "compact-basic", Enabled: &on, Config: map[string]any{"auto": false, "headroom": nil}}, &rev)
	if err != nil {
		t.Fatal(err)
	}
	if now, _ := Revision(path); now != rev2 || rev2 == rev {
		t.Fatalf("新 revision %s / %s", now, rev2)
	}
	if b, _ := os.ReadFile(path + ".bak"); string(b) != src {
		t.Fatal("应备份原文件")
	}
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]component.Entry{}
	for _, e := range p.Plugins {
		got[e.Name] = e
	}
	if e := got["web"]; e.Enabled == nil || !*e.Enabled {
		t.Fatalf("其他插件保留：%+v", e)
	}
	c := got["compact-basic"]
	if c.Enabled == nil || !*c.Enabled || c.Config["auto"] != false || c.Config["headroom"] != nil {
		t.Fatalf("改后的插件：%+v", c)
	}
	if p.Kernel["max_tool_calls_per_step"] != int64(16) || len(p.Mandatory) != 1 {
		t.Fatalf("kernel / mandatory 保留：%+v %v", p.Kernel, p.Mandatory)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), `include = ["base.toml"]`) || strings.Contains(string(b), "disabled") {
		t.Fatalf("重写结果：\n%s", b)
	}
	// 开关写回缺省、配置删光：这一行去掉。
	if _, err := Edit(path, component.PluginEdit{Name: "compact-basic", Config: map[string]any{"auto": nil}, Enabled: nil}, nil); err != nil {
		t.Fatal(err)
	}
	// 新文件
	np := filepath.Join(dir, "sub", "profile.toml")
	empty := ""
	if _, err := Edit(np, component.PluginEdit{Name: "web", Enabled: &on}, &empty); err != nil {
		t.Fatal(err)
	}
	q, err := Load(np)
	if err != nil || len(q.Plugins) != 1 || q.Plugins[0].Name != "web" {
		t.Fatalf("新建：%+v %v", q, err)
	}
	if _, err := Edit(np, component.PluginEdit{Name: "web", Enabled: &on}, &empty); !errors.Is(err, component.ErrRevision) {
		t.Fatalf("文件已存在时 if_revision 为空应冲突：%v", err)
	}
}
