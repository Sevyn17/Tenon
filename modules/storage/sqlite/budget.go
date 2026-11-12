package sqlite

// 预算账本（框架设计 08 8.4）：按 budget_ref 累计 token 用量。表在核心迁移 v15。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Sevyn17/tenon/runtime/store"
)

// AddBudget 累加用量并返回累加后的总量（store.BudgetStore）。
// limit > 0 且超限时仍记账，但返回 store.ErrBudgetExceeded。
func (db *DB) AddBudget(ctx context.Context, ref string, delta store.BudgetUsage, limit uint64) (store.BudgetUsage, error) {
	if ref == "" {
		return store.BudgetUsage{}, fmt.Errorf("store: 预算记账缺 ref")
	}
	now := time.Now().UnixMilli()
	// UPSERT：一次往返完成累加，避免读改写竞态。
	if _, err := db.q().ExecContext(ctx, `INSERT INTO budgets(tenant, ref, input, output, cache_read, cache_write, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(tenant, ref) DO UPDATE SET
			input = input + excluded.input, output = output + excluded.output,
			cache_read = cache_read + excluded.cache_read, cache_write = cache_write + excluded.cache_write,
			updated_at = excluded.updated_at`,
		store.Tenant, ref, delta.Input, delta.Output, delta.CacheRead, delta.CacheWrite, now); err != nil {
		return store.BudgetUsage{}, err
	}
	got, err := db.BudgetTotal(ctx, ref)
	if err != nil {
		return got, err
	}
	if limit > 0 && got.Total() > limit {
		return got, store.ErrBudgetExceeded
	}
	return got, nil
}

// BudgetTotal 读一个账的累计；没有返回零值。
func (db *DB) BudgetTotal(ctx context.Context, ref string) (store.BudgetUsage, error) {
	var u store.BudgetUsage
	err := db.rq().QueryRowContext(ctx, `SELECT input, output, cache_read, cache_write FROM budgets
		WHERE tenant = ? AND ref = ?`, store.Tenant, ref).Scan(&u.Input, &u.Output, &u.CacheRead, &u.CacheWrite)
	if errors.Is(err, sql.ErrNoRows) {
		return store.BudgetUsage{}, nil
	}
	return u, err
}

// ResetBudget 清零一个账。
func (db *DB) ResetBudget(ctx context.Context, ref string) error {
	_, err := db.q().ExecContext(ctx, `DELETE FROM budgets WHERE tenant = ? AND ref = ?`, store.Tenant, ref)
	return err
}
