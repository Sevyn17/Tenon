// Package storesqlite 存储插件 store-sqlite（强制）：提供 service:db（会话存储 + 工件）与 service:sql（组件自有表）。
package storesqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"

	"tenon/runtime/assemble"
	"tenon/runtime/component"
	"tenon/runtime/store"
	"tenon/runtime/store/sqlite"
	"tenon/runtime/toolhub"
)

// Store 存储句柄（实现 runtime.DBHandle）：Setup 时登记，Start 时打开（plugin-manager §5.1）。
type Store struct {
	mu    sync.Mutex
	db    *sqlite.DB
	blobs *sqlite.Blobs
}

// DB 已打开的数据库；Start 之前返回 nil。
func (s *Store) DB() store.Store {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil // 不能返回带类型的 nil 接口
	}
	return s.db
}

// Blobs 工件存储。
func (s *Store) Blobs() toolhub.Artifacts {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.blobs == nil {
		return nil
	}
	return s.blobs
}

// raw 已打开的数据库；Start 之前为 nil。
func (s *Store) raw() *sqlite.DB {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db
}

// SQL 存储的 SQL 出口（store.SQL，框架设计 05 §5.7）：Start 之前各方法不可用。
func (s *Store) SQL() store.SQL { return sqlOut{s} }

type sqlOut struct{ h *Store }

func (o sqlOut) DB() *sql.DB {
	db := o.h.raw()
	if db == nil {
		return nil
	}
	return db.DB()
}

func (o sqlOut) Dialect() string { return "sqlite" }

func (o sqlOut) Migrate(ctx context.Context, owner string, ms []store.Migration) error {
	db := o.h.raw()
	if db == nil {
		return errors.New("store-sqlite: 尚未启动")
	}
	return db.Migrate(ctx, owner, ms)
}

// Config store-sqlite 配置。
type Config struct {
	Dir      string `cfg:"dir" default:".tenon" title:"存储目录" description:"state.db 与 blobs/ 所在目录"`
	SyncFull bool   `cfg:"sync_full" title:"每次提交 fsync" description:"synchronous=FULL：掉电也不丢最后一次提交" widget:"switch"`
}

type storePlugin struct {
	h   *Store
	cfg *Config
}

func init() { component.Register("store-sqlite", func() component.Component { return &storePlugin{} }) }

func (p *storePlugin) Meta() component.Meta {
	return component.Meta{Name: "store-sqlite", Version: "0.1.0", Default: true, Config: &Config{},
		Provides: []string{assemble.ServiceDB, component.ServiceSQL}}
}

func (p *storePlugin) Setup(cx *component.Registrar) error {
	p.h, p.cfg = &Store{}, cx.Config().(*Config)
	if err := cx.Provide("sql", p.h.SQL()); err != nil {
		return err
	}
	return cx.Provide("db", assemble.DBHandle(p.h))
}

func (p *storePlugin) Start(sc *component.Scope) error {
	cfg := p.cfg
	if cfg == nil {
		return errors.New("store-sqlite: 缺配置")
	}
	db, err := sqlite.Open(sc.Context(), filepath.Join(cfg.Dir, "state.db"), sqlite.Options{SyncFull: cfg.SyncFull})
	if err != nil {
		return err
	}
	blobs, err := db.NewBlobs(filepath.Join(cfg.Dir, "blobs"))
	if err != nil {
		db.Close()
		return err
	}
	p.h.mu.Lock()
	p.h.db, p.h.blobs = db, blobs
	p.h.mu.Unlock()
	sc.OnClose(func(context.Context) error {
		p.h.mu.Lock()
		defer p.h.mu.Unlock()
		p.h.db, p.h.blobs = nil, nil
		return db.Close()
	})
	return nil
}
