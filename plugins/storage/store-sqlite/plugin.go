// Package storesqlite 存储插件 store-sqlite（强制）：填四个存储插槽（同一个 DB），提供 service:db。
package storesqlite

import (
	"context"
	"errors"
	"path/filepath"
	"sync"

	"ybase-kernel/core/plugin"
	kr "ybase-kernel/core/runtime"
	"ybase-kernel/core/store/sqlite"
)

// Store 存储句柄（实现 runtime.DBHandle）：Setup 时登记，Start 时打开（plugin-manager §5.1）。
type Store struct {
	mu    sync.Mutex
	db    *sqlite.DB
	blobs *sqlite.Blobs
}

// DB 已打开的数据库；Start 之前返回 nil。
func (s *Store) DB() *sqlite.DB {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db
}

// Blobs 工件存储。
func (s *Store) Blobs() *sqlite.Blobs {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.blobs
}

// Config store-sqlite 配置。
type Config struct {
	Dir      string `cfg:"dir" default:".ybase" title:"存储目录" description:"state.db 与 blobs/ 所在目录"`
	SyncFull bool   `cfg:"sync_full" title:"每次提交 fsync" description:"synchronous=FULL：掉电也不丢最后一次提交" widget:"switch"`
}

type storePlugin struct {
	h   *Store
	cfg *Config
}

func init() { plugin.Register("store-sqlite", func() plugin.Plugin { return &storePlugin{} }) }

func (p *storePlugin) Meta() plugin.Meta {
	return plugin.Meta{Name: "store-sqlite", Version: "0.1.0", Default: true, Config: &Config{},
		Provides: []string{kr.SlotSessionStore, kr.SlotToolResultStore, kr.SlotBlobStore, kr.SlotAgentStore, kr.ServiceDB}}
}

func (p *storePlugin) Setup(cx *plugin.SetupCx) error {
	p.h, p.cfg = &Store{}, cx.Config().(*Config)
	for _, s := range []string{"session_store", "tool_result_store", "blob_store", "agent_store"} {
		if err := cx.FillSlot(s, p.h); err != nil {
			return err
		}
	}
	return cx.Provide("db", kr.DBHandle(p.h))
}

func (p *storePlugin) Start(sc *plugin.Scope) error {
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
