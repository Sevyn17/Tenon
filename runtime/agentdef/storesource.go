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

	"github.com/Sevyn17/tenon/runtime/store"
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

// IsTombstone 报告存储里的一个版本是不是墓碑（空串）。
//
// 墓碑表示"这一层从此不再提供该定义"：最新版本是墓碑时，Load 按不存在处理、List 跳过，
// 名字解析因此回落到下一层（如租户层副本删除后回到平台层的内置定义）。旧版本照样保留可查。
// 空串不可能是合法定义：Encode 至少含 agent.toml。
func IsTombstone(body string) bool { return body == "" }

// latest 读某个存储键的最新版本；最新是墓碑时返回包裹 ErrNotFound 的错误。
func (s *StoreSource) latest(ctx context.Context, key string) (string, error) {
	body, _, err := s.Reader.GetDefinition(ctx, s.kind(), key, 0)
	if errors.Is(err, store.ErrNotFound) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if IsTombstone(body) {
		return "", ErrNotFound
	}
	return body, nil
}

// List 实现 Source：平台层全部、scope.Tenant 的租户层、scope.User 非空时该用户的用户层。
// 最新版本是墓碑的定义不列出。
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
		var ref Ref
		switch {
		case strings.HasPrefix(id, "platform/"):
			name := strings.TrimPrefix(id, "platform/")
			if !ValidName(name) {
				continue
			}
			ref = Ref{Namespace: Platform, Name: name}
		case strings.HasPrefix(id, "tenant/"):
			name := strings.TrimPrefix(id, "tenant/")
			if !ownTenant || !ValidName(name) {
				continue
			}
			ref = Ref{Namespace: Tenant, Tenant: scope.Tenant, Name: name}
		case strings.HasPrefix(id, UserPrefix):
			u, n, ok := strings.Cut(strings.TrimPrefix(id, UserPrefix), "/")
			if !ok || !ValidName(u) || !ValidName(n) || !ownTenant || scope.User == "" || u != scope.User {
				continue
			}
			ref = Ref{Namespace: User, Tenant: scope.Tenant, User: u, Name: n}
		default:
			continue
		}
		if _, err := s.latest(ctx, id); errors.Is(err, ErrNotFound) {
			continue
		} else if err != nil {
			return nil, fmt.Errorf("agentdef: 读定义 %s：%w", ref, err)
		}
		out = append(out, ref)
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
	body, err := s.latest(ctx, key)
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

// Delete 给一份定义写墓碑版本（不删历史），返回墓碑的版本号。之后 Load 按不存在处理，
// 名字解析回落到下一层。定义本来不存在或已是墓碑时返回 ErrNotFound，不重复写。
func (s *StoreSource) Delete(ctx context.Context, ref Ref) (int, error) {
	if s.Reader == nil {
		return 0, errors.New("agentdef: StoreSource 未配置读取器")
	}
	key, err := s.key(ref)
	if err != nil {
		return 0, err
	}
	if _, err := s.latest(ctx, key); err != nil {
		return 0, fmt.Errorf("定义 %s：%w", ref, err)
	}
	v, _, err := s.Reader.PutDefinition(ctx, s.kind(), key, "")
	return v, err
}

// ByHash 按内容哈希（Agent.Version）取存储里的一份定义，用于审计还原"某会话当时用的定义"。
// 存储没实现 store.DefinitionHashLookup 时返回 ErrUnsupported；找不到或命中墓碑返回 ErrNotFound。
// 返回的定义按它所在的存储键解析，Agent.Version 恒等于 hash。
func (s *StoreSource) ByHash(ctx context.Context, hash string) (Agent, error) {
	hl, ok := s.Reader.(store.DefinitionHashLookup)
	if !ok {
		return Agent{}, errors.ErrUnsupported
	}
	id, body, _, err := hl.DefinitionByHash(ctx, s.kind(), hash)
	if errors.Is(err, store.ErrNotFound) || (err == nil && IsTombstone(body)) {
		return Agent{}, fmt.Errorf("定义哈希 %s：%w", hash, ErrNotFound)
	}
	if err != nil {
		return Agent{}, err
	}
	files, err := Decode([]byte(body))
	if err != nil {
		return Agent{}, fmt.Errorf("定义哈希 %s：%w", hash, err)
	}
	return Parse(id, files)
}
