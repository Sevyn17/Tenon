package tenon_test

// 客户端改进 M11 配置T18：skills.list / skills.setEnabled 用真实的 skills 插件端到端跑（经 App.API() 调协议命令），
// 停用后新会话的系统块里没有它，重新启用后回来；启停记录只写用户级目录。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon"
	"github.com/Sevyn17/tenon/appapi"
	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/runtime/pkgs"
	"github.com/Sevyn17/tenon/tenontest"
)

func asErr(t *testing.T, err error) *appapi.Error {
	t.Helper()
	var e *appapi.Error
	if !errors.As(err, &e) {
		t.Fatalf("错误不是 *Error：%v", err)
	}
	return e
}

func putSkill(t *testing.T, p, desc string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("---\ndescription: "+desc+"\n---\n正文\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// skillsBlock 新建一个会话，返回它系统提示里技能目录块的文字（没有技能块时为空）。
func skillsBlock(t *testing.T, app *tenontest.App, id string) string {
	t.Helper()
	ctx := context.Background()
	if _, err := app.API().Call(ctx, "session.create", []byte(`{"session":"`+id+`"}`)); err != nil {
		t.Fatal(err)
	}
	l, err := app.API().DB().Load(ctx, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range l.Snapshot.Header.Config.System {
		if b.Source == "skills" {
			return b.Text
		}
	}
	return ""
}

func TestSkillsCommands(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	t.Setenv("TENON_HOME", home)
	putSkill(t, filepath.Join(dir, ".tenon", "skills", "proj", "SKILL.md"), "项目技能")
	putSkill(t, filepath.Join(home, "skills", "mine", "SKILL.md"), "用户技能")
	pdir := filepath.Join(t.TempDir(), "demo")
	putSkill(t, filepath.Join(pdir, "skills", "pk", "SKILL.md"), "包技能")
	if err := os.WriteFile(filepath.Join(pdir, pkgs.ManifestName), []byte("name = \"demo\"\nversion = \"0.1.0\"\nskills = \"skills\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := pkgs.Parse(pdir, "user")
	if err != nil {
		t.Fatal(err)
	}
	app := tenontest.New(t, tenon.WithWorkdir(dir), tenon.WithProjectTrust(true), tenon.WithPackages([]*pkgs.Package{p}))
	ctx := context.Background()
	call := func(cmd, args string) any {
		t.Helper()
		out, err := app.API().Call(ctx, cmd, []byte(args))
		if err != nil {
			t.Fatalf("%s %s：%v", cmd, args, err)
		}
		return out
	}

	hello := call("hello", `{}`).(wire.HelloResult)
	if !slices.Contains(hello.Capabilities, "skills") {
		t.Fatalf("应报 skills 能力：%v", hello.Capabilities)
	}
	list := call("skills.list", `{}`).(wire.SkillsListResult)
	src := map[string]string{}
	for _, s := range list.Skills {
		src[s.Name] = s.Source
		if !s.Enabled || s.Path == "" || s.Description == "" || !s.ModelInvocable {
			t.Fatalf("缺省应启用并带齐字段：%+v", s)
		}
	}
	if src["proj"] != wire.SkillSourceProject || src["mine"] != wire.SkillSourceUser || src["pk"] != "package:demo" {
		t.Fatalf("来源不对：%v", src)
	}
	if list.Workspace == "" {
		t.Fatal("应给出工作区键")
	}
	if b := skillsBlock(t, app, "s1"); !strings.Contains(b, "- mine:") || !strings.Contains(b, "- proj:") {
		t.Fatalf("系统块：%q", b)
	}

	// 全局停用 mine：列表标出，新会话的系统块里没有它
	r := call("skills.setEnabled", `{"name":"mine","enabled":false}`).(wire.SkillsSetEnabledResult)
	if r.Skill.Enabled || r.Skill.DisabledScope != wire.SkillScopeGlobal || r.Skill.DisabledReason == "" || r.Skill.Source != wire.SkillSourceUser {
		t.Fatalf("停用结果：%+v", r.Skill)
	}
	if b := skillsBlock(t, app, "s2"); strings.Contains(b, "mine") || !strings.Contains(b, "- proj:") {
		t.Fatalf("停用后系统块不应有 mine：%q", b)
	}
	// 本工作区停用 proj
	r = call("skills.setEnabled", `{"name":"proj","enabled":false,"scope":"workspace"}`).(wire.SkillsSetEnabledResult)
	if r.Skill.Enabled || r.Skill.DisabledScope != wire.SkillScopeWorkspace {
		t.Fatalf("工作区停用：%+v", r.Skill)
	}
	for _, s := range call("skills.list", `{}`).(wire.SkillsListResult).Skills {
		if (s.Name == "pk") != s.Enabled {
			t.Fatalf("只剩 pk 启用：%+v", s)
		}
	}
	if b := skillsBlock(t, app, "s3"); strings.Contains(b, "mine") || strings.Contains(b, "proj") || !strings.Contains(b, "- pk:") {
		t.Fatalf("系统块：%q", b)
	}
	// 重新启用：回到系统块
	call("skills.setEnabled", `{"name":"mine","enabled":true}`)
	call("skills.setEnabled", `{"name":"proj","enabled":true,"scope":"workspace"}`)
	if b := skillsBlock(t, app, "s4"); !strings.Contains(b, "- mine:") || !strings.Contains(b, "- proj:") {
		t.Fatalf("重新启用后应回来：%q", b)
	}
	// 未知技能 not_found
	if _, err := app.API().Call(ctx, "skills.setEnabled", []byte(`{"name":"ghost","enabled":false}`)); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("未知技能：%v", err)
	} else if e := asErr(t, err); e.Code != wire.CodeNotFound {
		t.Fatalf("未知技能应 not_found：%+v", e)
	}

	// 启停记录在用户级目录，项目目录里只有原来的技能文件
	if _, err := os.Stat(filepath.Join(home, "skills.toml")); err != nil {
		t.Fatalf("记录应在用户级目录：%v", err)
	}
	var inProject []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			inProject = append(inProject, p)
		}
		return nil
	})
	if len(inProject) != 1 || filepath.Base(inProject[0]) != "SKILL.md" {
		t.Fatalf("项目目录不应被写：%v", inProject)
	}
}
