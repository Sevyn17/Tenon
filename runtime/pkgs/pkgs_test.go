package pkgs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"ybase/runtime/component"
)

type nopPlugin struct{}

func (nopPlugin) Meta() component.Meta {
	return component.Meta{Name: "pkgs-test-target", Config: &testCfg{}}
}
func (nopPlugin) Setup(cx *component.Registrar) error { return nil }
func (nopPlugin) Start(sc *component.Scope) error     { return nil }

func init() {
	component.Register("pkgs-test-target", func() component.Component { return nopPlugin{} })
	// 测试用清单段：真实的段（mcp 的 [[server]]、hooks 的 [[hook]]）由各组件登记，本包不认识它们（审查 P1-56）。
	RegisterSection(Section{Key: "widget", Owner: "pkgs-test-target", Label: "部件",
		Decode: func(_ string, md toml.MetaData, prim toml.Primitive) (any, error) {
			var ws []widget
			if err := md.PrimitiveDecode(prim, &ws); err != nil {
				return nil, err
			}
			for _, w := range ws {
				if w.Name == "" {
					return nil, errors.New("部件缺 name")
				}
			}
			return ws, nil
		},
		Summary: func(v any) string { ws, _ := v.([]widget); return fmt.Sprintf("部件 %d 个", len(ws)) }})
}

type widget struct {
	Name string `toml:"name"`
}

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

[[widget]]
name = "w1"

[config.pkgs-test-target]
x = 1
`, "skills/", "a.md", "sub/b.md")
	p, err := Parse(dir, ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	ws, ok := SectionValue[[]widget](p, "widget")
	if p.SkillsDir != filepath.Join(dir, "skills") || len(p.Prompts) != 2 || !ok || len(ws) != 1 || ws[0].Name != "w1" {
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
	for _, w := range []string{"部件 1 个", "技能", "提示词 2"} {
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
		"section":  {"name = \"section\"\n[[widget]]\nname = \"\"", "部件缺 name"},
		"sectypo":  {"name = \"sectypo\"\n[[widget]]\nname = \"w\"\nnmae = \"x\"", "未知字段"},
		"nosec":    {"name = \"nosec\"\n[[server]]\nname = \"a\"\ncommand = \"x\"", "没有组件处理这一段"},
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
