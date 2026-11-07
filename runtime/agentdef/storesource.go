package agentdef

// 存储定义来源（计划/01 T1b-2c、03 3.5 specs.Store）：定义包以 Encode 的规范字节串存进 definitions 表。
//
// definitions 表没有命名空间列，所以存储键把层编进 id：
//
//	platform/<名字>       平台层
//	tenant/<名字>         租户层（存储按租户分库，租户就是构造时的 Tenant）
//	user/<用户>/<名字>    用户层
//
// 存储键只在来源内部用；对外仍用 Ref / FullName。

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"tenon/runtime/store"
)

// defaultKind 定义类缺省值（definitions.kind）。
const defaultKind = "agent"

// DefinitionReader 定义类数据的读写能力（store.DefinitionStore 的结构子集，便于测试替身）。
type DefinitionReader interface {
	PutDefinition(ctx context.Context, kind, id, body string) (version int, hash string, err error)
	GetDefinition(ctx context.Context, kind, id string, version int) (string, int, error)
	ListDefinitions(ctx context.Context, kind string) ([]string, error)
}

// StoreSource 从存储读定义的来源。
type StoreSource struct {
	Reader DefinitionReader
	// Tenant 当前租户；租户层、用户层的引用必须属于它，否则按不存在处理。
	Tenant string
	// Kind 定义类，空取 "agent"。
	Kind string
}

// NewStoreSource 返回从存储读定义的来源；kind 空取 "agent"。
func NewStoreSource(r DefinitionReader, tenant, kind string) *StoreSource {
	if kind == "" {
		kind = defaultKind
	}
	return &StoreSource{Reader: r, Tenant: tenant, Kind: kind}
}

func (s *StoreSource) kind() string {
	if s.Kind == "" {
		return defaultKind
	}
	return s.Kind
}

// key 引用对应的存储键；别的租户的租户层 / 用户层引用按不存在处理（不暴露是否存在）。
func (s *StoreSource) key(r Ref) (string, error) {
	if err := r.Validate(); err != nil {
		return "", err
	}
	if r.Namespace != Platform && r.Tenant != s.Tenant {
		return "", fmt.Errorf("定义 %s：%w", r, ErrNotFound)
	}
	switch r.Namespace {
	case Platform:
		return "platform/" + r.Name, nil
	case Tenant:
		return "tenant/" + r.Name, nil
	default:
		return UserPrefix + r.User + "/" + r.Name, nil
	}
}

// List 实现 Source：平台层全部、scope.Tenant 的租户层、scope.User 非空时该用户的用户层。
func (s *StoreSource) List(ctx context.Context, scope Scope) ([]Ref, error) {
	if s.Reader == nil {
		return nil, errors.New("agentdef: StoreSource 未配置读取器")
	}
	ids, err := s.Reader.ListDefinitions(ctx, s.kind())
	if err != nil {
		return nil, fmt.Errorf("agentdef: 列定义：%w", err)
	}
	ownTenant := scope.Tenant != "" && scope.Tenant == s.Tenant
	var out []Ref
	for _, id := range ids {
		switch {
		case strings.HasPrefix(id, "platform/"):
			if name := strings.TrimPrefix(id, "platform/"); ValidName(name) {
				out = append(out, Ref{Namespace: Platform, Name: name})
			}
		case strings.HasPrefix(id, "tenant/"):
			if name := strings.TrimPrefix(id, "tenant/"); ownTenant && ValidName(name) {
				out = append(out, Ref{Namespace: Tenant, Tenant: scope.Tenant, Name: name})
			}
		case strings.HasPrefix(id, UserPrefix):
			u, n, ok := strings.Cut(strings.TrimPrefix(id, UserPrefix), "/")
			if !ok || !ValidName(u) || !ValidName(n) {
				continue
			}
			if ownTenant && scope.User != "" && u == scope.User {
				out = append(out, Ref{Namespace: User, Tenant: scope.Tenant, User: u, Name: n})
			}
		}
	}
	sortRefs(out)
	return out, nil
}

// Load 实现 Source：读最新版本、Decode、Parse、核对 id。
func (s *StoreSource) Load(ctx context.Context, ref Ref) (Agent, error) {
	if s.Reader == nil {
		return Agent{}, errors.New("agentdef: StoreSource 未配置读取器")
	}
	key, err := s.key(ref)
	if err != nil {
		return Agent{}, err
	}
	body, _, err := s.Reader.GetDefinition(ctx, s.kind(), key, 0)
	if errors.Is(err, store.ErrNotFound) {
		return Agent{}, fmt.Errorf("定义 %s：%w", ref, ErrNotFound)
	}
	if err != nil {
		return Agent{}, fmt.Errorf("定义 %s：%w", ref, err)
	}
	files, err := Decode([]byte(body))
	if err != nil {
		return Agent{}, fmt.Errorf("定义 %s：%w", ref, err)
	}
	a, err := Parse(ref.String(), files)
	if err != nil {
		return Agent{}, err
	}
	if err := checkID(ref, a); err != nil {
		return Agent{}, err
	}
	return a, nil
}

// Save 写入一份定义的新版本，返回版本号与内容哈希。
//
// 权限检查不在这里：用户层写入的越权拒绝由定义写入接口做（03 3.5 的写 API，T3 前由调用方保证）。
func (s *StoreSource) Save(ctx context.Context, ref Ref, files Files) (int, string, error) {
	if s.Reader == nil {
		return 0, "", errors.New("agentdef: StoreSource 未配置读取器")
	}
	key, err := s.key(ref)
	if err != nil {
		return 0, "", err
	}
	a, err := Parse(ref.String(), files)
	if err != nil {
		return 0, "", err
	}
	if err := checkID(ref, a); err != nil {
		return 0, "", err
	}
	return s.Reader.PutDefinition(ctx, s.kind(), key, string(Encode(files)))
}
