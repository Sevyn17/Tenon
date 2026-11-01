package agentdef

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Namespace 定义所在的命名空间（03 3.5，D7）。三个命名空间之间不做字段合并。
type Namespace int

const (
	// Platform 平台层：应用随程序发布，所有租户可见。
	Platform Namespace = iota + 1
	// Tenant 租户层：本租户可见；与平台同名时整份替换平台定义。
	Tenant
	// User 用户层：独立命名空间 user/<用户>/<名字>，只本人可见，覆盖不了平台、租户定义。
	User
)

func (n Namespace) String() string {
	switch n {
	case Platform:
		return "platform"
	case Tenant:
		return "tenant"
	case User:
		return "user"
	}
	return fmt.Sprintf("Namespace(%d)", int(n))
}

// UserPrefix 用户层全名前缀：user/<用户>/<名字>。
const UserPrefix = "user/"

// Scope 查找范围：当前租户与当前用户。User 为空时只看平台和租户层。
type Scope struct {
	Tenant string
	User   string
}

// Ref 指向一份定义。
type Ref struct {
	Namespace Namespace
	Tenant    string // 平台层为空
	User      string // 只有用户层有
	Name      string
}

// FullName 地址与 SDK 里用的名字：平台、租户层为裸名，用户层为 user/<用户>/<名字>。
func (r Ref) FullName() string {
	if r.Namespace == User {
		return UserPrefix + r.User + "/" + r.Name
	}
	return r.Name
}

func (r Ref) String() string {
	switch r.Namespace {
	case Platform:
		return "platform:" + r.Name
	case Tenant:
		return "tenant:" + r.Tenant + "/" + r.Name
	case User:
		return "tenant:" + r.Tenant + "/" + r.FullName()
	}
	return "invalid:" + r.Name
}

// Validate 检查引用各字段与命名空间是否匹配。
func (r Ref) Validate() error {
	if !ValidName(r.Name) {
		return fmt.Errorf("定义引用 %s：名字 %q 不合法", r, r.Name)
	}
	switch r.Namespace {
	case Platform:
		if r.Tenant != "" || r.User != "" {
			return fmt.Errorf("定义引用 %s：平台层不能带租户或用户", r)
		}
	case Tenant:
		if !ValidName(r.Tenant) || r.User != "" {
			return fmt.Errorf("定义引用 %s：租户层须带合法租户、不能带用户", r)
		}
	case User:
		if !ValidName(r.Tenant) || !ValidName(r.User) {
			return fmt.Errorf("定义引用 %s：用户层须带合法租户与用户", r)
		}
	default:
		return fmt.Errorf("定义引用 %s：未知命名空间", r)
	}
	return nil
}

// Source 定义来源（03 3.5 SpecSource）。实现只按命名空间存取，不做遮蔽与字段合并；
// 名字解析与可见性由 Resolve / Visible 统一处理。
type Source interface {
	// List 返回 scope 能看到的各层全部定义引用：平台层全部、scope.Tenant 的租户层、
	// scope.User 非空时该用户的用户层。被租户同名遮蔽的平台定义也在其中，去重见 Visible。
	List(ctx context.Context, scope Scope) ([]Ref, error)
	// Load 加载并校验一份定义；不存在返回包裹 ErrNotFound 的错误。
	Load(ctx context.Context, ref Ref) (Agent, error)
}

// ParseName 把地址里的名字拆成引用（未绑定 scope）：裸名返回 bare=true 与名字；
// user/<用户>/<名字> 返回用户层引用（Tenant 待填）。
func ParseName(s string) (ref Ref, bare bool, err error) {
	if rest, ok := strings.CutPrefix(s, UserPrefix); ok {
		u, n, ok := strings.Cut(rest, "/")
		if !ok || !ValidName(u) || !ValidName(n) {
			return Ref{}, false, fmt.Errorf("定义名 %q 不合法（用户层应为 user/<用户>/<名字>）", s)
		}
		return Ref{Namespace: User, User: u, Name: n}, false, nil
	}
	if !ValidName(s) {
		return Ref{}, false, fmt.Errorf("定义名 %q 不合法", s)
	}
	return Ref{Name: s}, true, nil
}

// Resolve 在 scope 下解析名字（03 3.5 名字解析）：
//   - 裸名只按 租户 → 平台 查找，不查用户层；
//   - user/<用户>/<名字> 只有 scope.User 就是该用户时才解析，否则一律 ErrNotFound（不暴露别人的定义是否存在）。
func Resolve(ctx context.Context, src Source, scope Scope, name string) (Agent, Ref, error) {
	r, bare, err := ParseName(name)
	if err != nil {
		return Agent{}, Ref{}, err
	}
	if !bare {
		if scope.User == "" || scope.User != r.User || scope.Tenant == "" {
			return Agent{}, Ref{}, fmt.Errorf("定义 %s：%w", name, ErrNotFound)
		}
		r.Tenant = scope.Tenant
		a, err := src.Load(ctx, r)
		return a, r, err
	}
	if scope.Tenant != "" {
		tr := Ref{Namespace: Tenant, Tenant: scope.Tenant, Name: r.Name}
		a, err := src.Load(ctx, tr)
		if err == nil {
			return a, tr, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return Agent{}, Ref{}, err
		}
	}
	pr := Ref{Namespace: Platform, Name: r.Name}
	a, err := src.Load(ctx, pr)
	return a, pr, err
}

// Visible 返回 scope 下可用的定义引用：租户层同名遮蔽平台层（整份替换）；用户层独立列出。
// 顺序：平台 / 租户合并后按名字排序，其后是用户层按名字排序。
func Visible(ctx context.Context, src Source, scope Scope) ([]Ref, error) {
	refs, err := src.List(ctx, scope)
	if err != nil {
		return nil, err
	}
	shadow := map[string]bool{}
	for _, r := range refs {
		if r.Namespace == Tenant && r.Tenant == scope.Tenant {
			shadow[r.Name] = true
		}
	}
	var out []Ref
	for _, r := range refs {
		switch r.Namespace {
		case Platform:
			if !shadow[r.Name] {
				out = append(out, r)
			}
		case Tenant:
			if r.Tenant == scope.Tenant {
				out = append(out, r)
			}
		case User:
			if scope.User != "" && r.Tenant == scope.Tenant && r.User == scope.User {
				out = append(out, r)
			}
		}
	}
	sortRefs(out)
	return out, nil
}
