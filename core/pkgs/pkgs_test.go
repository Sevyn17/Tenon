package pkgs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ybase-kernel/core/plugin"
)

type nopPlugin struct{}

func (nopPlugin) Meta() plugin.Meta                { return plugin.Meta{Name: "pkgs-test-target", Config: &testCfg{}} }
func (nopPlugin) Setup(cx *plugin.Registrar) error { return nil }
func (nopPlugin) Start(sc *plugin.Scope) error     { return nil }

func init() { plugin.Register("pkgs-test-target", func() plugin.Plugin { return nopPlugin{} }) }

type testCfg struct {
	X int `cfg:"x"`
}

// mk 在 root/<name> 写清单与附带文件。
func mk(t *testing.T, root, name, manifest string, files ...string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		p := filepath.Join(dir, f)
		if strings.HasSuffix(f, "/") {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte("片段 "+f), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestName), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestParseFull(t *testing.T) {
	root := t.TempDir()
	dir := mk(t, root, "demo", `
name = "demo"
version = "1.0.0"
description = "演示"
skills = "skills"
prompt = ["a.md", "sub/b.md"]

[[server]]
name = "fs"
command = "${YBASE_PLUGIN_DIR}/bin/fs"
trust = "readonly"
sandbox = "workspace-write"

[[hook]]
event = "pre_tool_use"
matcher = "shell"
command = "echo hi"

[config.pkgs-test-target]
x = 1
`, "skills/", "a.md", "sub/b.md")
	p, err := Parse(dir, ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	if p.SkillsDir != filepath.Join(dir, "skills") || len(p.Prompts) != 2 || len(p.Server) != 1 || len(p.Hook) != 1 {
		t.Fatalf("解析结果不对：%+v", p)
	}
	txt, err := p.PromptText()
	if err != nil || !strings.Contains(txt, "片段 a.md\n\n片段 sub/b.md") {
		t.Fatalf("PromptText=%q err=%v", txt, err)
	}
	q := p.Profile()
	if len(q.Plugins) != 1 || q.Plugins[0].Name != "pkgs-test-target" || q.Plugins[0].Enabled != nil || q.Sources["pkgs-test-target"] != p.File {
		t.Fatalf("Profile 不对：%+v", q)
	}
	c := p.Contents()
	for _, w := range []string{"fs", "钩子 1", "技能", "提示词 2"} {
		if !strings.Contains(c, w) {
			t.Fatalf("Contents %q 缺 %q", c, w)
		}
	}
	if got := Expand("${YBASE_PLUGIN_DIR}/x", dir); got != dir+"/x" {
		t.Fatalf("Expand=%q", got)
	}
}

func TestParseErrors(t *testing.T) {
	cases := map[string]struct{ manifest, want string }{
		"bad-toml": {`name = `, ManifestName},
		"unknown":  {"name = \"unknown\"\nfoo = 1", "未知字段 foo"},
		"mismatch": {`name = "other"`, "须与目录名"},
		"escape":   {"name = \"escape\"\nprompt = [\"../x.md\"]", "跳出了包目录"},
		"absolute": {"name = \"absolute\"\nskills = \"C:/x\"", "skills"},
		"missing":  {"name = \"missing\"\nprompt = [\"none.md\"]", "不存在"},
		"both":     {"name = \"both\"\n[[server]]\nname = \"a\"\ncommand = \"x\"\nurl = \"http://x\"", "command 或 url"},
		"trust":    {"name = \"trust\"\n[[server]]\nname = \"a\"\ncommand = \"x\"\ntrust = \"all\"", "trust"},
		"event":    {"name = \"event\"\n[[hook]]\nevent = \"nope\"\ncommand = \"x\"", "不支持"},
		"matcher":  {"name = \"matcher\"\n[[hook]]\nevent = \"stop\"\ncommand = \"x\"\nmatcher = \"(\"", "matcher"},
		"noplugin": {"name = \"noplugin\"\n[config.no-such-plugin]\nx = 1", "没有这个插件"},
		"Bad_Name": {`name = "Bad_Name"`, "不合法"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir := mk(t, t.TempDir(), name, c.manifest)
			_, err := Parse(dir, ScopeUser)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err=%v，想要含 %q", err, c.want)
			}
			if !strings.Contains(err.Error(), filepath.Join(dir, ManifestName)) {
				t.Fatalf("错误没带清单路径：%v", err)
			}
		})
	}
}

func TestLoadTrust(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	mk(t, filepath.Join(home, "plugins"), "u", `name = "u"`)
	mk(t, filepath.Join(home, "plugins"), "same", `name = "same"`)
	os.MkdirAll(filepath.Join(home, "plugins", "not-a-package"), 0o755)
	proj := filepath.Join(root, ".ybase", "plugins")
	mk(t, proj, "p", `name = "p"`)
	mk(t, proj, "same", "name = \"same\"\nversion = \"2\"")

	ps, notes, err := Load(home, root, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 2 || ps[0].Name != "same" || ps[1].Name != "u" {
		t.Fatalf("未受信只应加载用户级：%v", names(ps))
	}
	if !strings.Contains(strings.Join(notes, "\n"), "项目未受信，跳过项目插件包") || !strings.Contains(strings.Join(notes, "\n"), "not-a-package") {
		t.Fatalf("notes=%v", notes)
	}

	ps, notes, err = Load(home, root, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(names(ps), ","); got != "u,p,same" {
		t.Fatalf("受信顺序=%s", got)
	}
	if ps[2].Scope != ScopeProject || ps[2].Version != "2" || !strings.Contains(strings.Join(notes, "\n"), "覆盖用户级") {
		t.Fatalf("项目级应覆盖用户级：%+v %v", ps[2], notes)
	}
}

func TestLoadDupServer(t *testing.T) {
	home := t.TempDir()
	m := "name = \"%s\"\n[[server]]\nname = \"fs\"\ncommand = \"x\""
	mk(t, filepath.Join(home, "plugins"), "a", strings.Replace(m, "%s", "a", 1))
	mk(t, filepath.Join(home, "plugins"), "b", strings.Replace(m, "%s", "b", 1))
	if _, _, err := Load(home, t.TempDir(), true); err == nil || !strings.Contains(err.Error(), "重复") {
		t.Fatalf("应报 MCP 服务重名：%v", err)
	}
}

func TestBadManifestStopsLoad(t *testing.T) {
	home := t.TempDir()
	dir := mk(t, filepath.Join(home, "plugins"), "bad", `name = 1`)
	_, _, err := Load(home, t.TempDir(), true)
	if err == nil || !strings.Contains(err.Error(), filepath.Join(dir, ManifestName)) {
		t.Fatalf("坏清单应报错并带文件名：%v", err)
	}
}

func TestScaffold(t *testing.T) {
	root := t.TempDir()
	dir, err := Scaffold(root, "my-pkg")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(dir, ScopeUser); err != nil {
		t.Fatalf("骨架应能直接解析：%v", err)
	}
	if _, err := Scaffold(root, "my-pkg"); err == nil {
		t.Fatal("重复生成应报错")
	}
	if _, err := Scaffold(root, "Bad"); err == nil {
		t.Fatal("非法名应报错")
	}
}

func names(ps []*Package) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Name)
	}
	return out
}
