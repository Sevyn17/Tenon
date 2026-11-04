package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSkillsDiscoverAndLoad(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	writeSkill(t, filepath.Join(a, "pdf", "SKILL.md"), "---\nname: pdf\ndescription: \"处理 PDF\"\n---\n# PDF\n见 scripts/x.py\n")
	writeSkill(t, filepath.Join(a, "flat.md"), "---\ndescription: >\n  多行\n  描述\n---\n正文\n")
	writeSkill(t, filepath.Join(a, "bad", "SKILL.md"), "---\nname: Bad_Name\ndescription: x\n---\n")
	writeSkill(t, filepath.Join(a, "nodesc", "SKILL.md"), "---\nname: nodesc\n---\n")
	writeSkill(t, filepath.Join(a, "hidden", "SKILL.md"), "---\ndescription: 不给模型\ndisable-model-invocation: true\n---\n")
	writeSkill(t, filepath.Join(a, "deep", "x", "SKILL.md"), "---\nname: deep\ndescription: 不递归\n---\n")
	writeSkill(t, filepath.Join(b, "pdf", "SKILL.md"), "---\nname: pdf\ndescription: 被覆盖\n---\n")
	s := &Skills{Roots: []string{a, b}}
	s.Scan()
	var names []string
	for _, sk := range s.List() {
		names = append(names, sk.Name)
	}
	if got := strings.Join(names, ","); got != "flat,hidden,pdf" {
		t.Fatalf("names %s warnings %v", got, s.Warnings)
	}
	if len(s.Warnings) != 3 { // bad / nodesc / 同名
		t.Fatalf("warnings %v", s.Warnings)
	}
	cat := s.Catalog()
	if !strings.Contains(cat, "- pdf: 处理 PDF") || !strings.Contains(cat, "- flat: 多行 描述") || strings.Contains(cat, "hidden") {
		t.Fatal(cat)
	}
	body, err := s.Load("pdf")
	if err != nil || !strings.Contains(body, "# PDF") || !strings.Contains(body, filepath.Join(a, "pdf")) {
		t.Fatal(body, err)
	}
	if _, err := s.Load("hidden"); err == nil {
		t.Fatal("hidden loaded")
	}
	if _, err := s.Load("nope"); err == nil {
		t.Fatal("unknown loaded")
	}
	// 装载时重读：名字被改则拒绝
	writeSkill(t, filepath.Join(a, "pdf", "SKILL.md"), "---\nname: other\ndescription: x\n---\n")
	if _, err := s.Load("pdf"); err == nil {
		t.Fatal("stale loaded")
	}
}

func TestSkillsCatalogBudget(t *testing.T) {
	d := t.TempDir()
	for _, n := range []string{"a1", "a2", "a3"} {
		writeSkill(t, filepath.Join(d, n, "SKILL.md"), "---\ndescription: "+strings.Repeat("x", 300)+"\n---\n")
	}
	s := &Skills{Roots: []string{d}, Budget: 500}
	s.Scan()
	cat := s.Catalog()
	if !strings.Contains(cat, "- a1:") || !strings.Contains(cat, "只列名字）a2, a3") {
		t.Fatal(cat)
	}
	if (&Skills{}).Catalog() != "" || (&Skills{}).Block() != nil {
		t.Fatal("empty catalog")
	}
}

// 不受信项目不读项目里的技能，只读用户级与插件包（审查 P1-4）。
func TestSkillsUntrustedProject(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	t.Setenv("YBASE_HOME", home)
	writeSkill(t, filepath.Join(root, ".ybase", "skills", "evil", "SKILL.md"), "---\nname: evil\ndescription: 项目技能\n---\n")
	writeSkill(t, filepath.Join(home, "skills", "mine", "SKILL.md"), "---\nname: mine\ndescription: 用户技能\n---\n")
	names := func(s *Skills) string {
		var out []string
		for _, k := range s.List() {
			out = append(out, k.Name)
		}
		return strings.Join(out, ",")
	}
	if got := names(NewSkillsTrust(root, false)); got != "mine" {
		t.Fatalf("不受信项目应只有用户技能: %q", got)
	}
	if got := names(NewSkillsTrust(root, true)); !strings.Contains(got, "evil") || !strings.Contains(got, "mine") {
		t.Fatalf("受信项目应两者都有: %q", got)
	}
}
