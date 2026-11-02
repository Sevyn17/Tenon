// Package sqlite：本地默认存储（data-contract D1）。单个 SQLite 文件，WAL 模式；
// 四个存储插槽由同一个 DB 填满，checkpoint 是一个事务。
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"ybase-kernel/core/bus"
	"ybase-kernel/core/store"
)

// DB 一个存储文件。
type DB struct {
	sql      *sql.DB
	tx       *sql.Tx // 非空表示当前是 Tx 内的事务视图（05：按子存储访问 + Tx）
	events   []bus.Envelope
	mu       sync.Mutex
	onCommit func(bus.Envelope)
}

// execer 是 *sql.DB 与 *sql.Tx 的公共子集，子存储方法通过 q() 取执行器。
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// q 返回当前执行器：事务视图内走 tx，否则直连。
func (db *DB) q() execer {
	if db.tx != nil {
		return db.tx
	}
	return db.sql
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
	// busy_timeout 排在第一个：pragma 按顺序执行，后面的语句才有等待。
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(%s)&_pragma=foreign_keys(ON)&_txlock=immediate", filepath.ToSlash(path), sync)
	// 新文件首次切 WAL 要独占锁，SQLite 在这一步不走 busy 等待：多个进程同时首次打开时
	// 后到者立即 SQLITE_BUSY。按退避重试整个打开（含迁移），上限见约束清单。
	var lastErr error
	for i := 0; i < openRetries; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(openBackoff << min(i-1, 5)):
			}
		}
		db, err := openOnce(ctx, dsn)
		if err == nil {
			return db, nil
		}
		lastErr = err
		if !isBusy(err) {
			return nil, err
		}
	}
	return nil, lastErr
}

// 并发首次打开的重试（约束清单「存储」）：最多 openRetries 次，退避从 openBackoff 起翻倍、封顶 32 倍。
const (
	openRetries = 20
	openBackoff = 10 * time.Millisecond
)

func openOnce(ctx context.Context, dsn string) (*DB, error) {
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

// isBusy 是否是 SQLite 的锁冲突（SQLITE_BUSY / SQLITE_LOCKED 及其扩展码）。
func isBusy(err error) bool {
	var se interface{ Code() int }
	if errors.As(err, &se) {
		switch se.Code() & 0xff {
		case 5, 6: // SQLITE_BUSY, SQLITE_LOCKED
			return true
		}
	}
	return false
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

// Migration 插件迁移（与 store.Migration 同一类型）。
type Migration = store.Migration

// SQLOut SQL 出口（store.SQL，框架设计 05 §5.7）。
func (db *DB) SQLOut() store.SQL { return sqlOut{db} }

type sqlOut struct{ db *DB }

func (o sqlOut) DB() *sql.DB     { return o.db.sql }
func (o sqlOut) Dialect() string { return "sqlite" }
func (o sqlOut) Migrate(ctx context.Context, owner string, ms []store.Migration) error {
	return o.db.Migrate(ctx, owner, ms)
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
		tx, err := db.sql.BeginTx(ctx, nil) // _txlock=immediate：开事务即持写锁，多进程迁移串行
		if err != nil {
			return err
		}
		// 拿到写锁后重读版本：另一个进程可能已在我们读版本之后做完了这一步（并发首次打开）。
		var now int
		if err := tx.QueryRowContext(ctx, `SELECT version FROM schema_versions WHERE owner = ?`, owner).Scan(&now); err != nil && err != sql.ErrNoRows {
			tx.Rollback()
			return err
		}
		if m.version <= now {
			tx.Rollback()
			cur = now
			continue
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
