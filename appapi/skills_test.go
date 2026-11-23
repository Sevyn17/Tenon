package appapi

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Sevyn17/tenon/appapi/wire"
)

type fakeSkills struct{ set []wire.SkillsSetEnabledArgs }

func (f *fakeSkills) List(context.Context) (wire.SkillsListResult, error) {
	return wire.SkillsListResult{}, nil
}

// readerSkills 另实现 SkillReader（skills.get）。
type readerSkills struct {
	fakeSkills
	ws   string
	body string
}

func (r *readerSkills) List(context.Context) (wire.SkillsListResult, error) {
	return wire.SkillsListResult{Workspace: r.ws}, nil
}

func (r *readerSkills) Body(_ context.Context, name string) (wire.SkillInfo, string, error) {
	if name != "pdf" {
		return wire.SkillInfo{}, "", Errorf(wire.CodeNotFound, "no skill")
	}
	return wire.SkillInfo{Name: name}, r.body, nil
}

// skills.get：实现没给 SkillReader 时 unsupported；缺名字 invalid；工作区不符 conflict；超上限按 UTF-8 截断；viewer 可调。
func TestSkillsGet(t *testing.T) {
	app := newAppOpts(t, func(o *Options) { o.Skills = &fakeSkills{} })
	_, e := call2(t, app, "skills.get", `{"name":"pdf"}`)
	wantCode(t, e, wire.CodeUnsupported, "")

	rs := &readerSkills{ws: "/w", body: "# 标题\n正文"}
	app = newAppOpts(t, func(o *Options) { o.Skills = rs })
	_, e = call2(t, app, "skills.get", `{}`)
	wantCode(t, e, wire.CodeInvalid, "")
	_, e = call2(t, app, "skills.get", `{"name":"pdf","workspace":"/other"}`)
	wantCode(t, e, wire.CodeConflict, "")
	_, e = call2(t, app, "skills.get", `{"name":"nope"}`)
	wantCode(t, e, wire.CodeNotFound, "")
	out, e := call2(t, app, "skills.get", `{"name":" pdf ","workspace":"/w"}`)
	if e != nil {
		t.Fatal(e)
	}
	r := out.(wire.SkillsGetResult)
	if r.Body != rs.body || r.Truncated || r.Size != len(rs.body) || r.Skill.Name != "pdf" {
		t.Fatalf("正文不符：%+v", r)
	}

	rs.body = strings.Repeat("中", wire.SkillBodyMax) // 3 字节一个，远超上限
	out, e = call2(t, app, "skills.get", `{"name":"pdf"}`)
	if e != nil {
		t.Fatal(e)
	}
	r = out.(wire.SkillsGetResult)
	if !r.Truncated || len(r.Body) > wire.SkillBodyMax || !utf8.ValidString(r.Body) || r.Size != len(rs.body) {
		t.Fatalf("应按 UTF-8 截断：len=%d truncated=%v size=%d", len(r.Body), r.Truncated, r.Size)
	}

	v := app.NewConn(context.Background(), wire.Principal{ID: "v", Role: wire.RoleViewer})
	defer v.Close()
	if _, err := v.Call(context.Background(), "skills.get", []byte(`{"name":"pdf"}`)); err != nil {
		t.Fatalf("viewer skills.get：%v", err)
	}
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
