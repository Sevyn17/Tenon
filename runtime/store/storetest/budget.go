package storetest

// 预算账本一致性用例（框架设计 08 8.4）：两个实现同语义。存储不支持 BudgetStore 时跳过。

import (
	"context"
	"errors"
	"testing"

	"github.com/Sevyn17/tenon/runtime/store"
)

func testBudgetLedger(t *testing.T, st store.Store) {
	bs, ok := st.(store.BudgetStore)
	if !ok {
		t.Skip("存储不支持预算账本")
	}
	ctx := context.Background()

	// 初始为 0。
	if u, err := bs.BudgetTotal(ctx, "r1"); err != nil || u.Total() != 0 {
		t.Fatalf("初始账应为 0：%v %v", u, err)
	}
	// 累加。
	if got, err := bs.AddBudget(ctx, "r1", store.BudgetUsage{Input: 10, Output: 5}, 0); err != nil || got.Total() != 15 {
		t.Fatalf("第一次累加 = %v %v", got, err)
	}
	if got, err := bs.AddBudget(ctx, "r1", store.BudgetUsage{Input: 3, CacheRead: 2}, 0); err != nil || got.Total() != 20 {
		t.Fatalf("第二次累加 = %v %v", got, err)
	}
	if u, _ := bs.BudgetTotal(ctx, "r1"); u.Total() != 20 {
		t.Fatalf("读回 = %v", u)
	}
	// 账之间隔离。
	if u, _ := bs.BudgetTotal(ctx, "r2"); u.Total() != 0 {
		t.Fatalf("r2 应为 0：%v", u)
	}
	// 超限：仍记账但返回 ErrBudgetExceeded。
	if got, err := bs.AddBudget(ctx, "r1", store.BudgetUsage{Input: 100}, 50); !errors.Is(err, store.ErrBudgetExceeded) {
		t.Fatalf("超限应返回 ErrBudgetExceeded，got=%v err=%v", got, err)
	}
	if u, _ := bs.BudgetTotal(ctx, "r1"); u.Total() != 120 {
		t.Fatalf("超限也要记账（便于核对）= %v", u)
	}
	// 未超限不报错。
	if _, err := bs.AddBudget(ctx, "r2", store.BudgetUsage{Input: 1}, 50); err != nil {
		t.Fatalf("未超限不该报错：%v", err)
	}
	// 清零。
	if err := bs.ResetBudget(ctx, "r1"); err != nil {
		t.Fatal(err)
	}
	if u, _ := bs.BudgetTotal(ctx, "r1"); u.Total() != 0 {
		t.Fatalf("清零后 = %v", u)
	}
	// 清零不存在的账不报错。
	if err := bs.ResetBudget(ctx, "nope"); err != nil {
		t.Fatalf("清零不存在的账不该报错：%v", err)
	}
	// 缺 ref 报错。
	if _, err := bs.AddBudget(ctx, "", store.BudgetUsage{Input: 1}, 0); err == nil {
		t.Fatal("缺 ref 应报错")
	}
}
