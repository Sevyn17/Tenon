package sqlite

import (
	"context"

	"github.com/Sevyn17/tenon/runtime/store"
)

// Tx（05）：需要原子写多条记录时用 Tx 包起来。

// Tx 在一个数据库事务里执行 fn。fn 拿到的 Store 视图的所有子存储写入都落在同一事务；
// Commit 产生的持久事件在事务提交成功后统一派发。已在事务里时直接加入外层事务。
//
// fn 里只能用传进来的 Store 视图：连接池只有一个连接（单写者），事务占着它，这时再用外层
// 句柄等不到连接。事务期间外层句柄的调用最多等 TxConnWait，等不到返回 ErrOuterInTx
// （别的 goroutine 的调用通常在事务结束后就拿到连接；fn 自己误用外层句柄则不再卡死，C8）。
func (db *DB) Tx(ctx context.Context, fn func(store.Store) error) error {
	if db.tx != nil {
		return fn(db)
	}
	t, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	clone := &DB{sql: db.sql, onCommit: db.onCommit, tx: t}
	db.txActive.Store(true)
	err = fn(clone)
	db.txActive.Store(false)
	if err != nil {
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
