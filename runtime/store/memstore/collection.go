package memstore

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/Sevyn17/tenon/runtime/store"
)

// 集合（框架设计 05 5.6，T4a）。语义与 sqlite 实现一致，供 storetest 对拍。

// collDoc 一条文档。Body 存编码后的 JSON，读出时再解码，避免与调用方共享 map。
type collDoc struct {
	version int64
	body    []byte
}

type collection struct {
	s           *Store
	spec        store.CollectionSpec
	id          string // tenant\x00owner\x00name
	owner, name string
}

func collKey(tenant, owner, name string) string {
	return tenant + "\x00" + owner + "\x00" + name
}

func docKey(part, key string) string { return part + "\x00" + key }

// Open 打开或登记一个集合（store.CollectionStore）。
func (s *Store) Open(ctx context.Context, tenant, owner, name string, spec store.CollectionSpec) (store.Collection, error) {
	spec, err := spec.Normalize()
	if err != nil {
		return nil, err
	}
	if err := store.CheckCollectionID(tenant, owner, name); err != nil {
		return nil, err
	}
	if err := s.lock(); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	id := collKey(tenant, owner, name)
	if old, ok := s.collSpecs[id]; ok {
		if !store.SameSpec(old, spec) {
			return nil, fmt.Errorf("store: 集合 %s.%s 已按不同结构登记", owner, name)
		}
	} else {
		s.collSpecs[id] = spec
		if s.collDocs[id] == nil {
			s.collDocs[id] = map[string]*collDoc{}
		}
	}
	if s.collVers[id] != spec.Version {
		return nil, fmt.Errorf("store: 集合 %s.%s 迁移停在版本 %d，登记的是 %d", owner, name, s.collVers[id], spec.Version)
	}
	return &collection{s: s, spec: spec, id: id, owner: owner, name: name}, nil
}

// BindCollection 返回句柄，不写集合登记（store.CollectionStore）。迁移用。
func (s *Store) BindCollection(ctx context.Context, tenant, owner, name string, spec store.CollectionSpec) (store.Collection, error) {
	spec, err := spec.Normalize()
	if err != nil {
		return nil, err
	}
	if err := store.CheckCollectionID(tenant, owner, name); err != nil {
		return nil, err
	}
	if err := s.lock(); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	id := collKey(tenant, owner, name)
	if s.collDocs[id] == nil {
		s.collDocs[id] = map[string]*collDoc{}
	}
	return &collection{s: s, spec: spec, id: id, owner: owner, name: name}, nil
}

func (c *collection) Get(ctx context.Context, part, key string) (store.Doc, error) {
	if err := c.s.lock(); err != nil {
		return store.Doc{}, err
	}
	defer c.s.mu.Unlock()
	d := c.s.collDocs[c.id][docKey(part, key)]
	if d == nil {
		return store.Doc{}, store.ErrNotFound
	}
	return decodeCollDoc(key, d)
}

func (c *collection) Put(ctx context.Context, part string, doc store.Doc, ifVersion int64) error {
	raw, err := store.CheckDoc(c.spec, part, doc)
	if err != nil {
		return err
	}
	if err := c.s.lock(); err != nil {
		return err
	}
	defer c.s.mu.Unlock()
	docs := c.s.collDocs[c.id]
	k := docKey(part, doc.Key)
	have := docs[k]
	if ifVersion == 0 {
		if have != nil {
			return fmt.Errorf("%w：%s.%s %s", store.ErrConflict, c.owner, c.name, doc.Key)
		}
		docs[k] = &collDoc{version: 1, body: raw}
		return nil
	}
	if have == nil {
		return store.ErrNotFound
	}
	if have.version != ifVersion {
		return fmt.Errorf("%w：%s.%s %s 版本不符", store.ErrConflict, c.owner, c.name, doc.Key)
	}
	docs[k] = &collDoc{version: have.version + 1, body: raw}
	return nil
}

func (c *collection) Delete(ctx context.Context, part, key string, ifVersion int64) error {
	if err := c.s.lock(); err != nil {
		return err
	}
	defer c.s.mu.Unlock()
	docs := c.s.collDocs[c.id]
	k := docKey(part, key)
	have := docs[k]
	if have == nil {
		return store.ErrNotFound
	}
	if have.version != ifVersion {
		return fmt.Errorf("%w：%s.%s %s 版本不符", store.ErrConflict, c.owner, c.name, key)
	}
	delete(docs, k)
	return nil
}

func (c *collection) Query(ctx context.Context, part, index string, eq any, page store.Page) ([]store.Doc, store.Page, error) {
	if !hasIndex(c.spec, index) {
		return nil, store.Page{}, fmt.Errorf("store: 集合 %s.%s 没有索引 %s", c.owner, c.name, index)
	}
	if err := c.s.lock(); err != nil {
		return nil, store.Page{}, err
	}
	defer c.s.mu.Unlock()
	limit := store.PageLimit(page.Limit)
	var keys []string
	prefix := part + "\x00"
	for k, d := range c.s.collDocs[c.id] {
		key, ok := strings.CutPrefix(k, prefix)
		if !ok || key <= page.After {
			continue
		}
		doc, err := decodeCollDoc(key, d)
		if err != nil {
			return nil, store.Page{}, err
		}
		if !equalJSON(doc.Body[index], eq) {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var out []store.Doc
	for _, key := range keys {
		doc, err := decodeCollDoc(key, c.s.collDocs[c.id][docKey(part, key)])
		if err != nil {
			return nil, store.Page{}, err
		}
		out = append(out, doc)
		if len(out) == limit {
			return out, store.Page{After: key}, nil
		}
	}
	return out, store.Page{}, nil
}

// CollectionVersion 读集合迁移跑到的版本。没有记录返回 0。
func (s *Store) CollectionVersion(ctx context.Context, tenant, owner, name string) (int, error) {
	if err := s.lock(); err != nil {
		return 0, err
	}
	defer s.mu.Unlock()
	return s.collVers[collKey(tenant, owner, name)], nil
}

// SetCollectionVersion 记下集合迁移跑到的版本。
func (s *Store) SetCollectionVersion(ctx context.Context, tenant, owner, name string, version int) error {
	if err := s.lock(); err != nil {
		return err
	}
	defer s.mu.Unlock()
	s.collVers[collKey(tenant, owner, name)] = version
	return nil
}

func hasIndex(spec store.CollectionSpec, index string) bool {
	for _, ix := range spec.Indexes {
		if ix == index {
			return true
		}
	}
	return false
}

func decodeCollDoc(key string, d *collDoc) (store.Doc, error) {
	var m map[string]any
	if err := json.Unmarshal(d.body, &m); err != nil {
		return store.Doc{}, fmt.Errorf("store: 集合文档 %s 正文读不出：%w", key, err)
	}
	return store.Doc{Key: key, Version: d.version, Body: m}, nil
}

func equalJSON(stored, query any) bool {
	a, _ := json.Marshal(stored)
	b, _ := json.Marshal(query)
	return string(a) == string(b)
}

var _ store.CollectionStore = (*Store)(nil)
