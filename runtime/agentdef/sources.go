package agentdef

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"sync"
)

// sortRefs 平台 / 租户层按名字排序在前，用户层按名字排序在后。
func sortRefs(rs []Ref) {
	sort.SliceStable(rs, func(i, j int) bool {
		ui, uj := rs[i].Namespace == User, rs[j].Namespace == User
		if ui != uj {
			return !ui
		}
		if rs[i].Name != rs[j].Name {
			return rs[i].Name < rs[j].Name
		}
		return rs[i].Namespace < rs[j].Namespace
	})
}

// checkID 定义包里的 id 须与引用名一致，防止目录名与内容不符。
func checkID(ref Ref, a Agent) error {
	if a.ID != ref.Name {
		return fmt.Errorf("定义 %s：id %q 须与名字 %q 相同", ref, a.ID, ref.Name)
	}
	return nil
}

// ---------- 目录来源 ----------

// DirSource 从目录加载定义（桌面版、本地开发）。目录布局：
//
//	<root>/platform/<名字>/agent.toml
//	<root>/tenant/<租户>/<名字>/agent.toml
//	<root>/user/<租户>/<用户>/<名字>/agent.toml
//
// 每次 Load 都重新读盘，改了文件即得到新 Version。
type DirSource struct {
	Root string
}

// Dir 返回以 root 为根的目录来源。
func Dir(root string) *DirSource { return &DirSource{Root: root} }

// dir 引用对应的包目录。
func (d *DirSource) dir(r Ref) string {
	switch r.Namespace {
	case Platform:
		return filepath.Join(d.Root, "platform", r.Name)
	case Tenant:
		return filepath.Join(d.Root, "tenant", r.Tenant, r.Name)
	default:
		return filepath.Join(d.Root, "user", r.Tenant, r.User, r.Name)
	}
}

// List 实现 Source。
func (d *DirSource) List(ctx context.Context, scope Scope) ([]Ref, error) {
	var out []Ref
	add := func(base string, mk func(name string) Ref) error {
		ents, err := os.ReadDir(base)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, e := range ents {
			if !e.IsDir() || !ValidName(e.Name()) {
				continue
			}
			if st, err := os.Stat(filepath.Join(base, e.Name(), ManifestName)); err != nil || !st.Mode().IsRegular() {
				continue
			}
			out = append(out, mk(e.Name()))
		}
		return nil
	}
	if err := add(filepath.Join(d.Root, "platform"), func(n string) Ref {
		return Ref{Namespace: Platform, Name: n}
	}); err != nil {
		return nil, err
	}
	if ValidName(scope.Tenant) {
		if err := add(filepath.Join(d.Root, "tenant", scope.Tenant), func(n string) Ref {
			return Ref{Namespace: Tenant, Tenant: scope.Tenant, Name: n}
		}); err != nil {
			return nil, err
		}
		if ValidName(scope.User) {
			if err := add(filepath.Join(d.Root, "user", scope.Tenant, scope.User), func(n string) Ref {
				return Ref{Namespace: User, Tenant: scope.Tenant, User: scope.User, Name: n}
			}); err != nil {
				return nil, err
			}
		}
	}
	sortRefs(out)
	return out, nil
}

// Load 实现 Source。
func (d *DirSource) Load(ctx context.Context, ref Ref) (Agent, error) {
	if err := ref.Validate(); err != nil {
		return Agent{}, err
	}
	dir := d.dir(ref)
	if st, err := os.Stat(filepath.Join(dir, ManifestName)); err != nil || !st.Mode().IsRegular() {
		return Agent{}, fmt.Errorf("定义 %s：%w", ref, ErrNotFound)
	}
	files, err := ReadDir(dir)
	if err != nil {
		return Agent{}, fmt.Errorf("定义 %s：%w", ref, err)
	}
	a, err := Parse(filepath.ToSlash(dir), files)
	if err != nil {
		return Agent{}, err
	}
	if err := checkID(ref, a); err != nil {
		return Agent{}, err
	}
	return a, nil
}

// ReadDir 把目录读成定义包文件集合（路径 / 分隔）。跳过符号链接与以 . 开头的文件和目录；
// 总大小超过 MaxPackageSize 报错。
func ReadDir(dir string) (Files, error) {
	files := Files{}
	total := 0
	err := filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == dir {
			return nil
		}
		if e.Name()[0] == '.' {
			if e.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if e.IsDir() || !e.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		total += len(b)
		if total > MaxPackageSize {
			return fmt.Errorf("定义包超过上限 %d 字节", MaxPackageSize)
		}
		files[path.Clean(filepath.ToSlash(rel))] = b
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// ---------- 内存来源 ----------

// MemSource 内存来源（应用代码直接给定义包、单测）。并发安全。
type MemSource struct {
	mu   sync.RWMutex
	pkgs map[Ref]Files
}

// Mem 返回空的内存来源。
func Mem() *MemSource { return &MemSource{pkgs: map[Ref]Files{}} }

// Put 放入或整份替换一份定义包；先按 Parse 校验，失败不保存。
func (m *MemSource) Put(ref Ref, files Files) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	cp := make(Files, len(files))
	for n, b := range files {
		cp[n] = append([]byte(nil), b...)
	}
	a, err := Parse(ref.String(), cp)
	if err != nil {
		return err
	}
	if err := checkID(ref, a); err != nil {
		return err
	}
	m.mu.Lock()
	m.pkgs[ref] = cp
	m.mu.Unlock()
	return nil
}

// Delete 删除一份定义；不存在时无操作。
func (m *MemSource) Delete(ref Ref) {
	m.mu.Lock()
	delete(m.pkgs, ref)
	m.mu.Unlock()
}

// List 实现 Source。
func (m *MemSource) List(ctx context.Context, scope Scope) ([]Ref, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Ref
	for r := range m.pkgs {
		switch r.Namespace {
		case Platform:
			out = append(out, r)
		case Tenant:
			if scope.Tenant != "" && r.Tenant == scope.Tenant {
				out = append(out, r)
			}
		case User:
			if scope.Tenant != "" && scope.User != "" && r.Tenant == scope.Tenant && r.User == scope.User {
				out = append(out, r)
			}
		}
	}
	sortRefs(out)
	return out, nil
}

// Load 实现 Source。
func (m *MemSource) Load(ctx context.Context, ref Ref) (Agent, error) {
	m.mu.RLock()
	files, ok := m.pkgs[ref]
	m.mu.RUnlock()
	if !ok {
		return Agent{}, fmt.Errorf("定义 %s：%w", ref, ErrNotFound)
	}
	return Parse(ref.String(), files)
}

var (
	_ Source = (*DirSource)(nil)
	_ Source = (*MemSource)(nil)
)
