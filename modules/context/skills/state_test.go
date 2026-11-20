package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 三种来源：项目（受信）、用户级、插件包（带包名）。
func TestSkillsSources(t *testing.T) {
	root, home, pkg := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("TENON_HOME", home)
	writeSkill(t, filepath.Join(root, ".tenon", "skills", "proj", "SKILL.md"), "---\ndescription: 项目\n---\n")
	writeSkill(t, filepath.Join(root, ".agents", "skills", "agents.md"), "---\ndescription: 项目 agents\n---\n")
	writeSkill(t, filepath.Join(home, "skills", "mine", "SKILL.md"), "---\ndescription: 用户\n---\n")
	writeSkill(t, filepath.Join(pkg, "pk", "SKILL.md"), "---\ndescription: 包\n---\n")
	s := NewSkillsPackages(root, true, []PackageDir{{Name: "demo", Dir: pkg}})
	got := map[string]string{}
	for _, sk := range s.List() {
		got[sk.Name] = sk.Source
	}
	want := map[string]string{"proj": SourceProject, "agents": SourceProject, "mine": SourceUser, "pk": "package:demo"}
	for n, src := range want {
		if got[n] != src {
			t.Fatalf("%s 来源应为 %s：%v", n, src, got)
		}
	}
	if s.State == nil || s.State.Workspace != WorkspaceKey(root) {
		t.Fatalf("工作区键：%+v", s.State)
	}
}

// 全局停用、工作区覆盖、重新启用；停用的不进目录、不能装载；记录只写用户级文件。
func TestSkillsStateScopes(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	t.Setenv("TENON_HOME", home)
	writeSkill(t, filepath.Join(home, "skills", "pdf", "SKILL.md"), "---\ndescription: 处理 PDF\n---\n正文\n")
	writeSkill(t, filepath.Join(home, "skills", "doc", "SKILL.md"), "---\ndescription: 处理文档\n---\n正文\n")
	s := NewSkillsTrust(root, true)
	other := NewSkillsTrust(t.TempDir(), true) // 另一个工作区，同一份用户级记录
	if cat := s.Catalog(); !strings.Contains(cat, "- pdf:") {
		t.Fatal(cat)
	}
	status := func(s *Skills, n string) Status {
		is, err := s.Infos()
		if err != nil {
			t.Fatal(err)
		}
		for _, i := range is {
			if i.Name == n {
				return i.Status
			}
		}
		t.Fatalf("没有 %s", n)
		return Status{}
	}

	if err := s.State.Set("pdf", false, ScopeGlobal); err != nil {
		t.Fatal(err)
	}
	if st := status(s, "pdf"); st.Enabled || st.Scope != ScopeGlobal || st.Reason() == "" {
		t.Fatalf("全局停用：%+v", st)
	}
	if cat := s.Catalog(); strings.Contains(cat, "pdf") || !strings.Contains(cat, "- doc:") {
		t.Fatal("停用的技能不应进目录：", cat)
	}
	if _, err := s.Load("pdf"); err == nil {
		t.Fatal("停用的技能不应能装载")
	}
	if status(other, "pdf").Enabled {
		t.Fatal("全局停用对别的工作区也生效")
	}

	// 本工作区覆盖启用：别的工作区仍停用
	if err := s.State.Set("pdf", true, ScopeWorkspace); err != nil {
		t.Fatal(err)
	}
	if !status(s, "pdf").Enabled || status(other, "pdf").Enabled {
		t.Fatal("工作区覆盖只影响本工作区")
	}
	if _, err := s.Load("pdf"); err != nil {
		t.Fatal(err)
	}
	// 本工作区停用 doc
	if err := s.State.Set("doc", false, ScopeWorkspace); err != nil {
		t.Fatal(err)
	}
	if st := status(s, "doc"); st.Enabled || st.Scope != ScopeWorkspace || !status(other, "doc").Enabled {
		t.Fatalf("工作区停用：%+v", st)
	}
	// 全局重新启用 pdf，再在工作区设成与全局一致：覆盖被删掉
	if err := s.State.Set("pdf", true, ScopeGlobal); err != nil {
		t.Fatal(err)
	}
	if err := s.State.Set("doc", true, ScopeWorkspace); err != nil {
		t.Fatal(err)
	}
	if err := s.State.Set("pdf", true, ScopeWorkspace); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(home, StateFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "pdf") || strings.Contains(string(b), "doc") {
		t.Fatalf("和全局一致后不应再有记录：\n%s", b)
	}
	if cat := s.Catalog(); !strings.Contains(cat, "- pdf:") || !strings.Contains(cat, "- doc:") {
		t.Fatal("重新启用后应回到目录：", cat)
	}
	// 项目目录里不写任何东西
	if _, err := os.Stat(filepath.Join(root, ".tenon")); !os.IsNotExist(err) {
		t.Fatalf("项目目录里不应出现 .tenon：%v", err)
	}
	if err := s.State.Set("pdf", true, "project"); err == nil {
		t.Fatal("未知 scope 应报错")
	}
}

// 记录文件坏了：按全部启用，并报错（skills.list 放进 warnings）。
func TestSkillsStateBroken(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TENON_HOME", home)
	writeSkill(t, filepath.Join(home, "skills", "pdf", "SKILL.md"), "---\ndescription: x\n---\n")
	if err := os.WriteFile(filepath.Join(home, StateFile), []byte("[global\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewSkillsTrust(t.TempDir(), true)
	is, err := s.Infos()
	if err == nil || len(is) != 1 || !is[0].Enabled {
		t.Fatalf("坏文件：%+v %v", is, err)
	}
	if err := s.State.Set("pdf", false, ScopeGlobal); err == nil {
		t.Fatal("坏文件不应被覆盖写")
	}
}
