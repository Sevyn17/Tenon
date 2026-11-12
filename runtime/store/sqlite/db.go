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
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"

	"github.com/Sevyn17/tenon/runtime/bus"
	"github.com/Sevyn17/tenon/runtime/store"
)

// DB 一个存储文件。
type DB struct {
	sql *sql.DB
	// rd 只读连接池（query_only）：WAL 下读不必排在写连接后面（2026-11-04 审查 P1-33：原先单连接读写串行，
	// Tx 超过 TxConnWait 时别的 goroutine 的合法读也报错）。nil = 没有读池（读走 sql）。
	rd       *sql.DB
	tx       *sql.Tx // 非空表示当前是 Tx 内的事务视图（05：按子存储访问 + Tx）
	events   []bus.Envelope
	mu       sync.Mutex
	onCommit func(bus.Envelope)
	// txActive 外层句柄上有 Tx 在进行（见 Tx / q）。
	txActive atomic.Bool
	path     string // 库文件（Open 时的路径）
}

// Path 库文件路径（Open 时给的）。
func (db *DB) Path() string { return db.path }

// execer 是 *sql.DB 与 *sql.Tx 的公共子集，子存储方法通过 q() 取执行器。
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// q 返回当前执行器：事务视图内走 tx，否则直连。Tx 进行中的外层句柄走 guarded（见 Tx）。
func (db *DB) q() execer {
	if db.tx != nil {
		return db.tx
	}
	if db.txActive.Load() {
		return guarded{db}
	}
	return db.sql
}

// rq 只读执行器：事务视图内走 tx（读到本事务的写入），否则走只读连接池。
func (db *DB) rq() execer {
	if db.tx != nil {
		return db.tx
	}
	if db.rd != nil {
		return db.rd
	}
	return db.q()
}

// ReadConns 只读连接池大小（约束清单「存储」）。
const ReadConns = 4

// ErrOuterInTx Tx 进行中用外层句柄、在 TxConnWait 内等不到连接（多半是 Tx 的 fn 误用了外层句柄）。
var ErrOuterInTx = errors.New("sqlite: 事务进行中，外层句柄等不到连接（Tx 的 fn 里请用传进来的 Store）")

// TxConnWait Tx 进行中外层句柄等连接的上限（约束清单「存储」）。
const TxConnWait = 2 * time.Second

// guarded Tx 进行中的外层执行器：先等 Tx 结束（最多 TxConnWait），等不到报 ErrOuterInTx，
// 而不是一直卡在连接池上。等到了再照常执行，查询本身不受这个期限约束。
type guarded struct{ db *DB }

// wait 等外层句柄上的 Tx 结束。
func (g guarded) wait(ctx context.Context) error {
	deadline := time.Now().Add(TxConnWait)
	for g.db.txActive.Load() {
		if time.Now().After(deadline) {
			return ErrOuterInTx
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
	return nil
}

func (g guarded) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	if err := g.wait(ctx); err != nil {
		return nil, err
	}
	return g.db.sql.ExecContext(ctx, q, args...)
}

func (g guarded) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	if err := g.wait(ctx); err != nil {
		return nil, err
	}
	return g.db.sql.QueryContext(ctx, q, args...)
}

// QueryRowContext 等不到时返回一个必然出错的 Row（*sql.Row 没有公开构造，借一个已取消的 ctx 产生错误）；
// 这时 Scan 报 context.Canceled。调用方据此知道没查成——比卡死好；Exec / Query 路径给的是 ErrOuterInTx。
func (g guarded) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	if err := g.wait(ctx); err != nil {
		c, cancel := context.WithCancel(ctx)
		cancel()
		return g.db.sql.QueryRowContext(c, q, args...)
	}
	return g.db.sql.QueryRowContext(ctx, q, args...)
}

// Options 打开选项。
type Options struct {
	// SyncFull：synchronous=FULL（默认 NORMAL：进程崩溃不丢提交，掉电可能丢最后一次）。
	SyncFull bool
}

// SQLiteStore 打开（没有就新建）一个 SQLite 会话库；应用 Close 时关闭它。
//
// 等价于 Open(ctx, path, Options{})；放在实现包里而不是根包，是为了让 import tenon
// 不必编进 SQLite 驱动（docs/02 - 开发管理/18 - 内核与 I/O 分离方案 第六步 1）。
func SQLiteStore(ctx context.Context, path string) (*DB, error) {
	return Open(ctx, path, Options{})
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
			rd, err := sql.Open("sqlite", dsn+"&_pragma=query_only(1)")
			if err != nil {
				db.sql.Close()
				return nil, err
			}
			rd.SetMaxOpenConns(ReadConns)
			db.rd, db.path = rd, path
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
func (db *DB) Close() error {
	if db.rd != nil {
		_ = db.rd.Close()
	}
	return db.sql.Close()
}

// DB 底层连接（store.SQL；组件自有表、测试用）。
func (db *DB) DB() *sql.DB { return db.sql }

// Dialect 方言名（store.SQL）。
func (db *DB) Dialect() string { return "sqlite" }

// *DB 自带 SQL 出口（store.SQL）：SDK 的 WithStore(sqlite) 据此装上要建表的组件（2026-11-04 审查 P2：
// 原先出口只在 SQLOut 包装里，tenon.New 断言不到，workgroup 被悄悄去掉）。
var _ store.SQL = (*DB)(nil)

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
