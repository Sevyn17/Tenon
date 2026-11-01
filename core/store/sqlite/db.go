// Package sqlite：本地默认存储（data-contract D1）。单个 SQLite 文件，WAL 模式；
// 四个存储插槽由同一个 DB 填满，checkpoint 是一个事务。
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite"

	"ybase-kernel/core/bus"
)

// DB 一个存储文件。
type DB struct {
	sql      *sql.DB
	mu       sync.Mutex
	onCommit func(bus.Envelope)
}

// Options 打开选项。
type Options struct {
	// SyncFull：synchronous=FULL（默认 NORMAL：进程崩溃不丢提交，掉电可能丢最后一次）。
	SyncFull bool
}

// Open 打开（不存在则创建）并执行迁移。
func Open(ctx context.Context, path string, opt Options) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	sync := "NORMAL"
	if opt.SyncFull {
		sync = "FULL"
	}
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=synchronous(%s)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_txlock=immediate", filepath.ToSlash(path), sync)
	s, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// 单写者：一个连接，写入天然串行（SQLite 单写线程语义）。
	s.SetMaxOpenConns(1)
	db := &DB{sql: s}
	if err := db.migrate(ctx, "core", coreMigrations); err != nil {
		s.Close()
		return nil, err
	}
	return db, nil
}

// Close 关闭。
func (db *DB) Close() error { return db.sql.Close() }

// SQL 暴露底层连接（插件 Db 服务、测试用）。
func (db *DB) SQL() *sql.DB { return db.sql }

// Migrate 执行某 owner 的迁移（插件自有表 p_<名>_* 经此登记，乙类点位）。
func (db *DB) Migrate(ctx context.Context, owner string, ms []Migration) error {
	in := make([]migration, len(ms))
	for i, m := range ms {
		in[i] = migration{version: m.Version, stmts: m.Stmts}
	}
	return db.migrate(ctx, owner, in)
}

// Migration 插件迁移。
type Migration struct {
	Version int
	Stmts   []string
}

func (db *DB) migrate(ctx context.Context, owner string, ms []migration) error {
	if _, err := db.sql.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_versions(owner TEXT NOT NULL PRIMARY KEY, version INTEGER NOT NULL)`); err != nil {
		return err
	}
	var cur int
	err := db.sql.QueryRowContext(ctx, `SELECT version FROM schema_versions WHERE owner = ?`, owner).Scan(&cur)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	for _, m := range ms {
		if m.version <= cur {
			continue
		}
		tx, err := db.sql.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		for _, st := range m.stmts {
			if _, err := tx.ExecContext(ctx, st); err != nil {
				tx.Rollback()
				return fmt.Errorf("迁移 %s v%d: %w", owner, m.version, err)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_versions(owner, version) VALUES(?, ?)
			ON CONFLICT(owner) DO UPDATE SET version = excluded.version`, owner, m.version); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		cur = m.version
	}
	return nil
}
