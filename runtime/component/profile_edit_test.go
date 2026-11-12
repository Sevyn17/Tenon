package component

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditProfile(t *testing.T) {
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
	if _, err := EditProfile(path, PluginEdit{Name: "compact-basic", Enabled: &on}, &stale); !errors.Is(err, ErrRevision) {
		t.Fatalf("revision 不符应 ErrRevision：%v", err)
	}
	rev2, err := EditProfile(path, PluginEdit{Name: "compact-basic", Enabled: &on, Config: map[string]any{"auto": false, "headroom": nil}}, &rev)
	if err != nil {
		t.Fatal(err)
	}
	if now, _ := Revision(path); now != rev2 || rev2 == rev {
		t.Fatalf("新 revision %s / %s", now, rev2)
	}
	if b, _ := os.ReadFile(path + ".bak"); string(b) != src {
		t.Fatal("应备份原文件")
	}
	p, err := LoadProfile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Entry{}
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
	if _, err := EditProfile(path, PluginEdit{Name: "compact-basic", Config: map[string]any{"auto": nil}, Enabled: nil}, nil); err != nil {
		t.Fatal(err)
	}
	// 新文件
	np := filepath.Join(dir, "sub", "profile.toml")
	empty := ""
	if _, err := EditProfile(np, PluginEdit{Name: "web", Enabled: &on}, &empty); err != nil {
		t.Fatal(err)
	}
	q, err := LoadProfile(np)
	if err != nil || len(q.Plugins) != 1 || q.Plugins[0].Name != "web" {
		t.Fatalf("新建：%+v %v", q, err)
	}
	if _, err := EditProfile(np, PluginEdit{Name: "web", Enabled: &on}, &empty); !errors.Is(err, ErrRevision) {
		t.Fatalf("文件已存在时 if_revision 为空应冲突：%v", err)
	}
}

func TestCheckConfigNormalizesInts(t *testing.T) {
	name := "zz-check-config"
	type cfg struct {
		N int     `cfg:"n"`
		F float64 `cfg:"f"`
	}
	registerOnce(name, func() Component { return metaOnly{Meta{Name: name, Config: &cfg{}}} })
	got, err := CheckConfig(name, map[string]any{"n": float64(3), "f": float64(2)})
	if err != nil || got["n"] != int64(3) || got["f"] != float64(2) {
		t.Fatalf("%#v %v", got, err)
	}
	if _, err := CheckConfig(name, map[string]any{"n": "x"}); err == nil {
		t.Fatal("类型不符应报错")
	}
	if _, err := CheckConfig(name, map[string]any{"zz": 1}); err == nil {
		t.Fatal("未知键应报错")
	}
	if _, err := CheckConfig("no-such-plugin", nil); err == nil {
		t.Fatal("未登记应报错")
	}
}

type metaOnly struct{ m Meta }

func (c metaOnly) Meta() Meta           { return c.m }
func (metaOnly) Setup(*Registrar) error { return nil }
func (metaOnly) Start(*Scope) error     { return nil }
