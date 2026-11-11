package host

// 预算记账（框架设计 08 8.4，T4f/T5c 的基础设施）：回合结束时把本回合的 token 用量记到
// 本轮的 budget_ref 账上；用尽时发 budget.exceeded 并转入预算挂起（02 2.2）。
//
// 账本是可选能力（store.BudgetStore）：存储没实现就跳过，不阻塞。
// 上限取本会话算好的使用配置 [use.budget]（三层取最小，会话打开时由 useconf 算好）；
// 本轮触发者的用户层重算依赖 ConfigSource（T4f 剩余，见 docs/02 - 开发管理/10）。

import (
	"context"
	"errors"

	"github.com/Sevyn17/tenon/runtime/store"
)

// turnUsage 本回合的 token 用量：从会话历史里取指定回合的 assistant 条目累加。
// turn <= 0 时取最后一条 assistant 条目所属回合（回合号在提交后可能已递增，调用方传当前值兜底）。
func (a *actor) turnUsage(turn uint32) store.BudgetUsage {
	var u store.BudgetUsage
	if a.ag == nil {
		return u
	}
	entries := a.ag.Committed().Entries
	if turn == 0 {
		// 取最后一条 assistant 的回合。
		for i := len(entries) - 1; i >= 0; i-- {
			if entries[i].Item.Assistant != nil {
				turn = entries[i].Turn
				break
			}
		}
	}
	for _, e := range entries {
		if e.Turn != turn || e.Item.Assistant == nil {
			continue
		}
		au := e.Item.Assistant.Usage
		u = u.Add(store.BudgetUsage{Input: au.Input, Output: au.Output,
			CacheRead: au.CacheRead, CacheWrite: au.CacheWrite})
	}
	return u
}

// budgetLimitOf 本会话 [use.budget] 的 token 上限（0 = 不限）。只认 tokens_per_day 一个键；
// 其它键（未来扩展）不影响记账。会话没配时用宿主级缺省上限（Host.DefaultBudget，应用可注入）。
func (a *actor) budgetLimitOf() uint64 {
	if a.h.DefaultBudget > 0 {
		if v := a.sessionBudgetLimit(); v > 0 {
			return v
		}
		return a.h.DefaultBudget
	}
	return a.sessionBudgetLimit()
}

// sessionBudgetLimit 本会话 [use.budget] 的 token 上限（0 = 未配）。
func (a *actor) sessionBudgetLimit() uint64 {
	if a.ag == nil || a.ag.Ctx == nil {
		return 0
	}
	v, ok := a.ag.Ctx.Use("budget")
	if !ok {
		return 0
	}
	m, ok := v.(map[string]any)
	if !ok {
		return 0
	}
	switch n := m["tokens_per_day"].(type) {
	case int64:
		if n > 0 {
			return uint64(n)
		}
	case float64:
		if n > 0 {
			return uint64(n)
		}
	case int:
		if n > 0 {
			return uint64(n)
		}
	}
	return 0
}

// accountBudget 回合结束后记一笔账；超限时发 budget.exceeded 并挂起会话。
// 记账失败只发 budget.failed，不影响回合结果（08 4.6 失败隔离）。
func (h *Host) accountBudget(ctx context.Context, id, ref string, turn uint32, usage store.BudgetUsage, limit uint64) {
	if ref == "" || usage.Total() == 0 {
		return
	}
	bs, ok := h.DB.(store.BudgetStore)
	if !ok {
		return // 存储不支持预算账本：跳过
	}
	_, err := bs.AddBudget(ctx, ref, usage, limit)
	if err == nil {
		return
	}
	if !errors.Is(err, store.ErrBudgetExceeded) {
		h.publish(id, "budget.failed", map[string]any{"ref": ref, "error": err.Error()})
		return
	}
	// 用尽：当前步已做完，不再发起新调用 → 转入预算挂起（02 2.2）。
	// 记账发生在 actor 协程里，不能同步调 Suspend（会往本 actor 投命令并等它，自锁）；
	// 走 Post 入队，回合结束后由 actor 自己处理。
	h.publish(id, "budget.exceeded", map[string]any{"ref": ref, "limit": limit})
	if hd, ok := h.Get(id); ok {
		_ = hd.Post(Command{Kind: Suspend, Text: "budget"})
	}
}
