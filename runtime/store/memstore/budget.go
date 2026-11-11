package memstore

// 预算账本（框架设计 08 8.4）：按 budget_ref 累计 token 用量。与 sqlite 的 budgets 表同语义。

import (
	"context"
	"fmt"
	"time"

	"github.com/Sevyn17/tenon/runtime/store"
)

// AddBudget 累加用量并返回累加后的总量（store.BudgetStore）。
func (s *Store) AddBudget(ctx context.Context, ref string, delta store.BudgetUsage, limit uint64) (store.BudgetUsage, error) {
	if ref == "" {
		return store.BudgetUsage{}, fmt.Errorf("store: 预算记账缺 ref")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return store.BudgetUsage{}, ErrClosed
	}
	if s.budgets == nil {
		s.budgets = map[string]*budgetRow{}
	}
	row := s.budgets[ref]
	if row == nil {
		row = &budgetRow{}
		s.budgets[ref] = row
	}
	row.usage = row.usage.Add(delta)
	row.updatedAt = time.Now().UnixMilli()
	got := row.usage
	if limit > 0 && got.Total() > limit {
		return got, store.ErrBudgetExceeded
	}
	return got, nil
}

// BudgetTotal 读一个账的累计；没有返回零值。
func (s *Store) BudgetTotal(ctx context.Context, ref string) (store.BudgetUsage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return store.BudgetUsage{}, ErrClosed
	}
	if row := s.budgets[ref]; row != nil {
		return row.usage, nil
	}
	return store.BudgetUsage{}, nil
}

// ResetBudget 清零一个账。
func (s *Store) ResetBudget(ctx context.Context, ref string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	delete(s.budgets, ref)
	return nil
}
