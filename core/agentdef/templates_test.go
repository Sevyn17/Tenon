package agentdef

import (
	"context"
	"sort"
	"strings"
	"testing"
)

// memWith 建一个内存来源并放好给定引用（内容包 id 与引用名一致）。
func memWith(t *testing.T, refs ...Ref) Source {
	t.Helper()
	m := Mem()
	for _, ref := range refs {
		if err := m.Put(ref, defFiles(ref.Name)); err != nil {
			t.Fatalf("Put %s：%v", ref, err)
		}
	}
	return m
}

func fullNames(refs []Ref) []string {
	out := make([]string, len(refs))
	for i, r := range refs {
		out[i] = r.FullName()
	}
	sort.Strings(out)
	return out
}

// tenantListSource 故意无视 Scope 列出引用，用来覆盖「组件来源没按 scope 过滤」的防御分支。
type tenantListSource struct{ refs []Ref }

func (s tenantListSource) List(context.Context, Scope) ([]Ref, error) { return s.refs, nil }

func (s tenantListSource) Load(context.Context, Ref) (Agent, error) { return Agent{}, ErrNotFound }

func TestWithTemplatesMerge(t *testing.T) {
	ctx := context.Background()
	app := memWith(t,
		Ref{Namespace: Platform, Name: "app"},
		Ref{Namespace: Tenant, Tenant: "t1", Name: "tenant-a"},
	)
	tpl := memWith(t, Ref{Namespace: Platform, Name: "tmpl"})
	src, err := WithTemplates(app, []Template{{Owner: "组件A", Source: tpl}})
	if err != nil {
		t.Fatalf("WithTemplates：%v", err)
	}

	refs, err := src.List(ctx, Scope{Tenant: "t1"})
	if err != nil {
		t.Fatalf("List：%v", err)
	}
	if got := strings.Join(fullNames(refs), ","); got != "app,tenant-a,tmpl" {
		t.Fatalf("平台+租户列举 = %s", got)
	}
	if refs, err := src.List(ctx, Scope{}); err != nil {
		t.Fatalf("List 平台层：%v", err)
	} else if got := strings.Join(fullNames(refs), ","); got != "app,tmpl" {
		t.Fatalf("平台层列举 = %s", got)
	}

	for _, ref := range []Ref{
		{Namespace: Platform, Name: "app"},
		{Namespace: Platform, Name: "tmpl"},
		{Namespace: Tenant, Tenant: "t1", Name: "tenant-a"},
	} {
		a, err := src.Load(ctx, ref)
		if err != nil {
			t.Fatalf("Load %s：%v", ref, err)
		}
		if a.ID != ref.Name {
			t.Fatalf("Load %s = id %q", ref, a.ID)
		}
	}

	// app 为 nil（应用没带平台定义）时，模板仍然可解析。
	only, err := WithTemplates(nil, []Template{{Owner: "组件A", Source: tpl}})
	if err != nil {
		t.Fatalf("app 为 nil：%v", err)
	}
	if _, err := only.Load(ctx, Ref{Namespace: Platform, Name: "tmpl"}); err != nil {
		t.Fatalf("app 为 nil 时 Load 模板：%v", err)
	}
	if _, err := only.Load(ctx, Ref{Namespace: Platform, Name: "nope"}); err == nil {
		t.Fatal("不存在的定义应报错")
	}
}

func TestWithTemplatesRejectsBadTemplates(t *testing.T) {
	// MemSource 列举空 scope 时只列平台层，所以租户层模板表现为「模板为空」；
	// 组件自定义来源仍可能把租户层定义列出来，那类由「只能放平台命名空间」拦下。
	tenantTpl := tenantListSource{refs: []Ref{{Namespace: Tenant, Tenant: "t1", Name: "x"}}}
	cases := []struct {
		name string
		app  Source
		ts   []Template
		want string
	}{
		{name: "nil 来源", app: memWith(t), ts: []Template{{Owner: "组件A"}}, want: "来源为空"},
		{name: "空模板", app: memWith(t), ts: []Template{{Owner: "组件A", Source: Mem()}}, want: "模板为空"},
		{
			name: "模板只含租户层", app: memWith(t),
			ts:   []Template{{Owner: "组件A", Source: memWith(t, Ref{Namespace: Tenant, Tenant: "t1", Name: "x"})}},
			want: "模板为空",
		},
		{name: "模板含租户层", app: memWith(t), ts: []Template{{Owner: "组件A", Source: tenantTpl}}, want: "只能放平台命名空间"},
		{
			name: "模板间同名", app: memWith(t),
			ts: []Template{
				{Owner: "组件A", Source: memWith(t, Ref{Namespace: Platform, Name: "x"})},
				{Owner: "组件B", Source: memWith(t, Ref{Namespace: Platform, Name: "x"})},
			},
			want: "同名",
		},
		{
			name: "与应用平台定义同名", app: memWith(t, Ref{Namespace: Platform, Name: "x"}),
			ts:   []Template{{Owner: "组件A", Source: memWith(t, Ref{Namespace: Platform, Name: "x"})}},
			want: "同名",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := WithTemplates(c.app, c.ts)
			if err == nil {
				t.Fatalf("应报错：%s", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("错误信息 = %q，期望含 %q", err, c.want)
			}
		})
	}
}

// 模板与应用租户层同名：装配不拦（应用可以后加租户定义），列举 / 解析时拒绝（09 9.3）。
func TestWithTemplatesTenantNameConflict(t *testing.T) {
	ctx := context.Background()
	app := memWith(t, Ref{Namespace: Tenant, Tenant: "t1", Name: "same"})
	src, err := WithTemplates(app, []Template{{Owner: "组件A", Source: memWith(t, Ref{Namespace: Platform, Name: "same"})}})
	if err != nil {
		t.Fatalf("装配阶段不应报错：%v", err)
	}
	if _, err := src.List(ctx, Scope{Tenant: "t1"}); err == nil {
		t.Fatal("列举租户层时应拒绝与模板同名的定义")
	}
	if _, err := src.Load(ctx, Ref{Namespace: Tenant, Tenant: "t1", Name: "same"}); err == nil {
		t.Fatal("解析租户层时应拒绝与模板同名的定义")
	}
	// 平台层照常可用。
	if _, err := src.Load(ctx, Ref{Namespace: Platform, Name: "same"}); err != nil {
		t.Fatalf("平台层 Load：%v", err)
	}
}
