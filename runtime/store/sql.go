package store

import (
	"context"
	"database/sql"
)

// SQL 存储的 SQL 出口（框架设计 05 §5.7）。只给声明了 Requires{SQL: true} 的组件用；
// 不是 SQL 的存储不实现它，装配时直接报错。
type SQL interface {
	// DB 底层连接。
	DB() *sql.DB
	// Dialect 方言名，如 "sqlite"。
	Dialect() string
	// Migrate 执行某 owner 的迁移（组件自有表 p_<名>_*）。
	Migrate(ctx context.Context, owner string, ms []Migration) error
}

// Migration 组件自有表的一次版本迁移。
type Migration struct {
	Version int
	Stmts   []string
}
