package appapi

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/Sevyn17/tenon/appapi/wire"
)

type fakeSkills struct{ set []wire.SkillsSetEnabledArgs }

func (f *fakeSkills) List(context.Context) (wire.SkillsListResult, error) {
	return wire.SkillsListResult{}, nil
}

func (f *fakeSkills) SetEnabled(_ context.Context, in wire.SkillsSetEnabledArgs) (wire.SkillsSetEnabledResult, error) {
	f.set = append(f.set, in)
	return wire.SkillsSetEnabledResult{Skill: wire.SkillInfo{Name: in.Name, Enabled: in.Enabled}}, nil
}

// 没装 skills：两条命令 unsupported，不报 skills 能力。
func TestSkillsUnsupported(t *testing.T) {
	app := newApp(t)
	if slices.Contains(app.capabilities(), "skills") {
		t.Fatal("没装 skills 不应报 skills")
	}
	for _, c := range []struct{ cmd, args string }{{"skills.list", `{}`}, {"skills.setEnabled", `{"name":"pdf"}`}} {
		_, e := call2(t, app, c.cmd, c.args)
		wantCode(t, e, wire.CodeUnsupported, "")
	}
}

// 能力、空列表给 []、scope 缺省 global、参数校验、viewer 只能 list。
func TestSkillsRolesAndArgs(t *testing.T) {
	fs := &fakeSkills{}
	app := newAppOpts(t, func(o *Options) { o.Skills = fs })
	if !slices.Contains(app.capabilities(), "skills") {
		t.Fatal("应报 skills 能力")
	}
	out, e := call2(t, app, "skills.list", ``)
	if e != nil || out.(wire.SkillsListResult).Skills == nil {
		t.Fatalf("空列表应为 []：%+v %v", out, e)
	}
	if _, e := call2(t, app, "skills.setEnabled", `{"name":" pdf ","enabled":false}`); e != nil {
		t.Fatal(e)
	}
	if len(fs.set) != 1 || fs.set[0].Name != "pdf" || fs.set[0].Scope != wire.SkillScopeGlobal {
		t.Fatalf("scope 缺省 global、名字去空白：%+v", fs.set)
	}
	for _, args := range []string{`{}`, `{"name":"pdf","scope":"project"}`} {
		_, e := call2(t, app, "skills.setEnabled", args)
		wantCode(t, e, wire.CodeInvalid, "")
	}
	v := app.NewConn(context.Background(), wire.Principal{ID: "v", Role: wire.RoleViewer})
	defer v.Close()
	if _, err := v.Call(context.Background(), "skills.list", nil); err != nil {
		t.Fatalf("viewer skills.list：%v", err)
	}
	_, err := v.Call(context.Background(), "skills.setEnabled", []byte(`{"name":"pdf","enabled":false}`))
	var pe *Error
	if !errors.As(err, &pe) || pe.Code != wire.CodeForbidden {
		t.Fatalf("viewer 不能 setEnabled：%v", err)
	}
	if len(fs.set) != 1 {
		t.Fatalf("viewer 的调用不应到达实现：%+v", fs.set)
	}
}
