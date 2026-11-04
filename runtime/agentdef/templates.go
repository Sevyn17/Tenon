package agentdef

// 组件内置定义模板（04 4.2 Registrar.Template、03 3.5、计划/01 T1b-2c）。
//
// 组件带来的模板归入平台命名空间，与应用随程序发布的平台定义合并：
//   - 模板之间同名 → 装配失败；
//   - 模板与应用平台定义同名 → 装配失败（03 3.5「启动报错」）；
//   - 应用租户层与模板同名 → 解析 / 列举时拒绝（09 9.3「与租户同名定义冲突时报错」）。

import (
	"context"
	"fmt"
)

// Template 一个组件带来的内置定义模板；Owner 是组件名（报错与 explain 用）。
type Template struct {
	Owner  string
	Source Source
}

// WithTemplates 把应用自带的平台定义与组件模板合成一个来源。
//
// app 可以是 nil（应用没有自带平台定义）；ts 为空时等价于直接用 app。
// 校验在装配时一次做完，失败即启动报错。
func WithTemplates(app Source, ts []Template) (Source, error) {
	ctx := context.Background()
	owners := make(map[string]string, len(ts))
	srcs := make(map[string]Source, len(ts))
	for _, t := range ts {
		if t.Source == nil {
			return nil, fmt.Errorf("定义模板（组件 %s）：来源为空", t.Owner)
		}
		refs, err := t.Source.List(ctx, Scope{})
		if err != nil {
			return nil, fmt.Errorf("定义模板（组件 %s）：列模板：%w", t.Owner, err)
		}
		if len(refs) == 0 {
			return nil, fmt.Errorf("定义模板（组件 %s）：模板为空", t.Owner)
		}
		for _, r := range refs {
			if r.Namespace != Platform {
				return nil, fmt.Errorf("定义模板（组件 %s）：%s 只能放平台命名空间", t.Owner, r)
			}
			if other, ok := owners[r.Name]; ok {
				return nil, fmt.Errorf("定义模板 %s：组件 %s 与组件 %s 的模板同名", r.Name, t.Owner, other)
			}
			owners[r.Name] = t.Owner
			srcs[r.Name] = t.Source
		}
	}
	if app != nil {
		refs, err := app.List(ctx, Scope{})
		if err != nil {
			return nil, fmt.Errorf("读取应用的平台定义：%w", err)
		}
		for _, r := range refs {
			if r.Namespace != Platform {
				continue
			}
			if owner, ok := owners[r.Name]; ok {
				return nil, fmt.Errorf("应用的平台定义 %s 与组件 %s 的模板同名", r.Name, owner)
			}
		}
	}
	return &templated{app: app, owners: owners, srcs: srcs}, nil
}

// templated 是应用定义来源与组件模板的合并视图。
type templated struct {
	app    Source
	owners map[string]string // 模板名 → 组件
	srcs   map[string]Source // 模板名 → 模板来源
}

// List 实现 Source：平台层取 app 与模板的并集；租户层 / 用户层原样转发（模板同名即报错）。
func (t *templated) List(ctx context.Context, scope Scope) ([]Ref, error) {
	var out []Ref
	if t.app != nil {
		refs, err := t.app.List(ctx, scope)
		if err != nil {
			return nil, err
		}
		for _, r := range refs {
			if r.Namespace == Tenant {
				if owner, ok := t.owners[r.Name]; ok {
					return nil, fmt.Errorf("定义 %s 与组件 %s 的模板同名：模板只能由组件提供", r, owner)
				}
			}
			out = append(out, r)
		}
	}
	for name := range t.owners {
		out = append(out, Ref{Namespace: Platform, Name: name})
	}
	sortRefs(out)
	return out, nil
}

// Load 实现 Source：平台层先查模板再查应用；租户层 / 用户层查应用，与模板同名即报错。
func (t *templated) Load(ctx context.Context, ref Ref) (Agent, error) {
	if err := ref.Validate(); err != nil {
		return Agent{}, err
	}
	if ref.Namespace == Platform {
		if src, ok := t.srcs[ref.Name]; ok {
			return src.Load(ctx, ref)
		}
		if t.app == nil {
			return Agent{}, fmt.Errorf("定义 %s：%w", ref, ErrNotFound)
		}
		return t.app.Load(ctx, ref)
	}
	if t.app == nil {
		return Agent{}, fmt.Errorf("定义 %s：%w", ref, ErrNotFound)
	}
	a, err := t.app.Load(ctx, ref)
	if err != nil {
		return Agent{}, err
	}
	// 租户层确实定义了同名定义才报冲突；裸名解析会先试租户层，不存在时照常落到平台层。
	if ref.Namespace == Tenant {
		if owner, ok := t.owners[ref.Name]; ok {
			return Agent{}, fmt.Errorf("定义 %s 与组件 %s 的模板同名：模板只能由组件提供", ref, owner)
		}
	}
	return a, nil
}

var _ Source = (*templated)(nil)
