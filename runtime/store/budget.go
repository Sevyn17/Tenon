package store

// 预算账本（框架设计 08 8.4，T4f/T5c 的基础设施）：按 budget_ref 累计用量。
//
// 不在 Store 上：第三方存储实现 Store 即可，账本是可选能力（与 PruneStore、CollectionStore、
// DefinitionStore 同一规则）。没实现 BudgetStore 的存储，预算记账跳过（不阻塞）。

import (
	"context"
	"errors"
	"time"
)

// ErrBudgetExceeded 用量已超过上限。
var ErrBudgetExceeded = errors.New("store: 预算已用尽")

// BudgetUsage 一次记账的用量增量（token）。四个字段按模型返回的 usage 累加。
type BudgetUsage struct {
	Input      uint64
	Output     uint64
	CacheRead  uint64
	CacheWrite uint64
}

// Total 四项之和（预算按总量算）。
func (u BudgetUsage) Total() uint64 { return u.Input + u.Output + u.CacheRead + u.CacheWrite }

// Add 两项相加。
func (u BudgetUsage) Add(o BudgetUsage) BudgetUsage {
	return BudgetUsage{Input: u.Input + o.Input, Output: u.Output + o.Output,
		CacheRead: u.CacheRead + o.CacheRead, CacheWrite: u.CacheWrite + o.CacheWrite}
}

// BudgetEntry 一个账上的累计。
type BudgetEntry struct {
	Ref       string
	Usage     BudgetUsage
	UpdatedAt time.Time
}

// BudgetStore 预算账本（可选能力）。
type BudgetStore interface {
	// AddBudget 把增量累加到 ref 的账上，返回累加后的总量。
	// limit > 0 且累加后超过 limit 时返回 ErrBudgetExceeded（用量仍记上，便于事后核对）。
	AddBudget(ctx context.Context, ref string, delta BudgetUsage, limit uint64) (BudgetUsage, error)
	// BudgetTotal 读 ref 的累计（没有返回零值）。
	BudgetTotal(ctx context.Context, ref string) (BudgetUsage, error)
	// ResetBudget 清零一个账（应用加额度或人确认后 Resume 时用）。没有该 ref 不报错。
	ResetBudget(ctx context.Context, ref string) error
}
