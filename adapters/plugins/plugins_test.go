package plugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/runtime/pkgs"
)

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
	if err := os.WriteFile(filepath.Join(dir, pkgs.ManifestName), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoadTrust(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	mk(t, filepath.Join(home, "plugins"), "u", `name = "u"`)
	mk(t, filepath.Join(home, "plugins"), "same", `name = "same"`)
	os.MkdirAll(filepath.Join(home, "plugins", "not-a-package"), 0o755)
	proj := filepath.Join(root, ".tenon", "plugins")
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
	if ps[2].Scope != pkgs.ScopeProject || ps[2].Version != "2" || !strings.Contains(strings.Join(notes, "\n"), "覆盖用户级") {
		t.Fatalf("项目级应覆盖用户级：%+v %v", ps[2], notes)
	}
}

func TestBadManifestStopsLoad(t *testing.T) {
	home := t.TempDir()
	dir := mk(t, filepath.Join(home, "plugins"), "bad", `name = 1`)
	_, _, err := Load(home, t.TempDir(), true)
	if err == nil || !strings.Contains(err.Error(), filepath.Join(dir, pkgs.ManifestName)) {
		t.Fatalf("坏清单应报错并带文件名：%v", err)
	}
}

func TestScaffold(t *testing.T) {
	root := t.TempDir()
	dir, err := Scaffold(root, "my-pkg")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pkgs.Parse(dir, pkgs.ScopeUser); err != nil {
		t.Fatalf("骨架应能直接解析：%v", err)
	}
	if _, err := Scaffold(root, "my-pkg"); err == nil {
		t.Fatal("重复生成应报错")
	}
	if _, err := Scaffold(root, "Bad"); err == nil {
		t.Fatal("非法名应报错")
	}
}

func names(ps []*pkgs.Package) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Name)
	}
	return out
}
