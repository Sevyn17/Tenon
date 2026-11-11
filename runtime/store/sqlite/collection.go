package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Sevyn17/tenon/runtime/store"
)

// 集合（框架设计 05 5.6，T4a）。表在核心迁移 v13。
//
// 索引字段存在 body JSON 里，Query 先按主键前缀取出分区，再在内存里等值过滤。
// 一个分区的文档量是组件数据，不是会话日志；真要索引时再加迁移，不在这里猜。

// collection 一个已打开的集合。db 是打开它的句柄：事务视图里打开的，写入落在该事务上。
type collection struct {
	db                  *DB
	spec                store.CollectionSpec
	tenant, owner, name string
}

// Open 打开或登记一个集合（store.CollectionStore）。
func (db *DB) Open(ctx context.Context, tenant, owner, name string, spec store.CollectionSpec) (store.Collection, error) {
	spec, err := spec.Normalize()
	if err != nil {
		return nil, err
	}
	if err := store.CheckCollectionID(tenant, owner, name); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	c := &collection{db: db, spec: spec, tenant: tenant, owner: owner, name: name}

	var have string
	err = db.q().QueryRowContext(ctx, `SELECT spec FROM collections
		WHERE tenant = ? AND owner = ? AND name = ?`, tenant, owner, name).Scan(&have)
	switch {
	case err == sql.ErrNoRows:
		if _, err := db.q().ExecContext(ctx, `INSERT INTO collections(tenant, owner, name, spec, version)
			VALUES(?, ?, ?, ?, ?)`, tenant, owner, name, string(raw), spec.Version); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	default:
		var old store.CollectionSpec
		if err := json.Unmarshal([]byte(have), &old); err != nil {
			return nil, fmt.Errorf("store: 集合 %s.%s 的已存结构读不出：%w", owner, name, err)
		}
		if !store.SameSpec(old, spec) {
			return nil, fmt.Errorf("store: 集合 %s.%s 已按不同结构登记", owner, name)
		}
	}
	// 迁移没跑完不能按新结构打开：数据还是旧形状，按新版本读会读错。
	got, err := db.CollectionVersion(ctx, tenant, owner, name)
	if err != nil {
		return nil, err
	}
	if got != spec.Version {
		return nil, fmt.Errorf("store: 集合 %s.%s 迁移停在版本 %d，登记的是 %d", owner, name, got, spec.Version)
	}
	return c, nil
}

// BindCollection 返回句柄，不写集合登记（store.CollectionStore）。迁移用。
func (db *DB) BindCollection(ctx context.Context, tenant, owner, name string, spec store.CollectionSpec) (store.Collection, error) {
	spec, err := spec.Normalize()
	if err != nil {
		return nil, err
	}
	if err := store.CheckCollectionID(tenant, owner, name); err != nil {
		return nil, err
	}
	return &collection{db: db, spec: spec, tenant: tenant, owner: owner, name: name}, nil
}

func (c *collection) Get(ctx context.Context, part, key string) (store.Doc, error) {
	var version int64
	var body string
	err := c.db.q().QueryRowContext(ctx, `SELECT version, body FROM collection_docs
		WHERE tenant = ? AND owner = ? AND name = ? AND part = ? AND key = ?`,
		c.tenant, c.owner, c.name, part, key).Scan(&version, &body)
	if err == sql.ErrNoRows {
		return store.Doc{}, store.ErrNotFound
	}
	if err != nil {
		return store.Doc{}, err
	}
	return decodeDoc(key, version, body)
}

func (c *collection) Put(ctx context.Context, part string, doc store.Doc, ifVersion int64) error {
	raw, err := store.CheckDoc(c.spec, part, doc)
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	if ifVersion == 0 {
		res, err := c.db.q().ExecContext(ctx, `INSERT INTO collection_docs(tenant, owner, name, part, key, version, body, updated_at)
			VALUES(?, ?, ?, ?, ?, 1, ?, ?) ON CONFLICT DO NOTHING`,
			c.tenant, c.owner, c.name, part, doc.Key, string(raw), now)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("%w：%s.%s %s", store.ErrConflict, c.owner, c.name, doc.Key)
		}
		return nil
	}
	res, err := c.db.q().ExecContext(ctx, `UPDATE collection_docs SET version = version + 1, body = ?, updated_at = ?
		WHERE tenant = ? AND owner = ? AND name = ? AND part = ? AND key = ? AND version = ?`,
		string(raw), now, c.tenant, c.owner, c.name, part, doc.Key, ifVersion)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return nil
	}
	// 没改到行：要么 key 不存在，要么版本不符。两者返回值不同，再读一次区分。
	if _, err := c.Get(ctx, part, doc.Key); err != nil {
		return err
	}
	return fmt.Errorf("%w：%s.%s %s 版本不符", store.ErrConflict, c.owner, c.name, doc.Key)
}

func (c *collection) Delete(ctx context.Context, part, key string, ifVersion int64) error {
	res, err := c.db.q().ExecContext(ctx, `DELETE FROM collection_docs
		WHERE tenant = ? AND owner = ? AND name = ? AND part = ? AND key = ? AND version = ?`,
		c.tenant, c.owner, c.name, part, key, ifVersion)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return nil
	}
	if _, err := c.Get(ctx, part, key); err != nil {
		return err
	}
	return fmt.Errorf("%w：%s.%s %s 版本不符", store.ErrConflict, c.owner, c.name, key)
}

func (c *collection) Query(ctx context.Context, part, index string, eq any, page store.Page) ([]store.Doc, store.Page, error) {
	if !hasIndex(c.spec, index) {
		return nil, store.Page{}, fmt.Errorf("store: 集合 %s.%s 没有索引 %s", c.owner, c.name, index)
	}
	limit := store.PageLimit(page.Limit)
	rows, err := c.db.q().QueryContext(ctx, `SELECT key, version, body FROM collection_docs
		WHERE tenant = ? AND owner = ? AND name = ? AND part = ? AND key > ?
		ORDER BY key`, c.tenant, c.owner, c.name, part, page.After)
	if err != nil {
		return nil, store.Page{}, err
	}
	defer rows.Close()
	var out []store.Doc
	for rows.Next() {
		var key, body string
		var version int64
		if err := rows.Scan(&key, &version, &body); err != nil {
			return nil, store.Page{}, err
		}
		doc, err := decodeDoc(key, version, body)
		if err != nil {
			return nil, store.Page{}, err
		}
		if !equalJSON(doc.Body[index], eq) {
			continue
		}
		out = append(out, doc)
		if len(out) == limit {
			return out, store.Page{After: key}, rows.Close()
		}
	}
	return out, store.Page{}, rows.Err()
}

// CollectionVersion 读集合迁移跑到的版本。没有记录返回 0。
func (db *DB) CollectionVersion(ctx context.Context, tenant, owner, name string) (int, error) {
	var v int
	err := db.q().QueryRowContext(ctx, `SELECT version FROM collection_versions
		WHERE tenant = ? AND owner = ? AND name = ?`, tenant, owner, name).Scan(&v)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return v, err
}

// SetCollectionVersion 记下集合迁移跑到的版本。
func (db *DB) SetCollectionVersion(ctx context.Context, tenant, owner, name string, version int) error {
	_, err := db.q().ExecContext(ctx, `INSERT INTO collection_versions(tenant, owner, name, version) VALUES(?, ?, ?, ?)
		ON CONFLICT(tenant, owner, name) DO UPDATE SET version = excluded.version`,
		tenant, owner, name, version)
	return err
}

func hasIndex(spec store.CollectionSpec, index string) bool {
	for _, ix := range spec.Indexes {
		if ix == index {
			return true
		}
	}
	return false
}

func decodeDoc(key string, version int64, body string) (store.Doc, error) {
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		return store.Doc{}, fmt.Errorf("store: 集合文档 %s 正文读不出：%w", key, err)
	}
	return store.Doc{Key: key, Version: version, Body: m}, nil
}

// equalJSON 等值比较。查询参数没经过 JSON 往返（int 对得上 float64），先归一再比。
func equalJSON(stored, query any) bool {
	a, _ := json.Marshal(stored)
	b, _ := json.Marshal(query)
	return string(a) == string(b)
}

// 编译期断言。
var _ store.CollectionStore = (*DB)(nil)
