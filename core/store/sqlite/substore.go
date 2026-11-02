package sqlite

import (
	"context"

	"ybase-kernel/core/store"
)

// 按子存储访问与 Tx（05 决定：随发件箱/session_pending 一起在 T3 落地）。
// 使用方只依赖自己用到的小接口；需要原子写多条记录时用 Tx 包起来。

func (db *DB) Sessions() store.SessionStore       { return db }
func (db *DB) Queue() store.InboxStore            { return db }
func (db *DB) Results() store.ToolResultStore     { return db }
func (db *DB) Definitions() store.DefinitionStore { return db }
func (db *DB) Leases() store.LeaseStore           { return db }
func (db *DB) Sleeps() store.SleepStore           { return db }
func (db *DB) Pending() store.CorrStore           { return db }
func (db *DB) OutboxQueue() store.OutboxStore     { return db }

// Tx 在一个数据库事务里执行 fn。fn 拿到的 Store 视图的所有子存储写入都落在同一事务；
// Commit 产生的持久事件在事务提交成功后统一派发。已在事务里时直接加入外层事务。
func (db *DB) Tx(ctx context.Context, fn func(store.Store) error) error {
	if db.tx != nil {
		return fn(db)
	}
	t, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	clone := &DB{sql: db.sql, onCommit: db.onCommit, tx: t}
	if err := fn(clone); err != nil {
		_ = t.Rollback()
		return err
	}
	if err := t.Commit(); err != nil {
		return err
	}
	db.notify(clone.events)
	return nil
}

var _ store.Store = (*DB)(nil)
