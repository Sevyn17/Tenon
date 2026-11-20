package host

// 排队的用户输入（客户端改进 M12 协议T20）。
//
// Send{Queue: true} 的输入落信箱时 kind 记为 queued：回合中的步边界不插入它（collectInboxBatch 只认 input），
// 当前回合结束后 drainInbox 按准入顺序逐条取，每条单独开一个回合。信箱是持久的，重启后照常处理。
// 取消（Unqueue）与开回合之间用 actor.mu 串行：drainInbox 取到排队项后先在锁内核对它仍未消费并登记为已认领，
// 之后的取消一律失败；取消在锁内先看认领表，再改存储。

import (
	"context"
	"errors"

	"github.com/Sevyn17/tenon/runtime/store"
)

// ErrQueueUnsupported 存储没有实现 store.QueueStore（不能取消排队）。
var ErrQueueUnsupported = errors.New("host: 存储不支持取消排队")

// EventQueueChanged 排队列表变化的易失事件（data: {"queued": []store.InboxItem}）。
const EventQueueChanged = "queue_changed"

// QueueSupported 存储是否支持排队（实现了 store.QueueStore）。不支持时 Send 的 Queue 由调用方忽略。
func (h *Host) QueueSupported() bool {
	_, ok := h.DB.(store.QueueStore)
	return ok
}

// Queued 会话里还在排队、没开始处理的输入（按准入顺序）。已被 drainInbox 认领、正要开回合的不算。
func (h *Host) Queued(ctx context.Context, id string) ([]store.InboxItem, error) {
	items, err := h.DB.Inbox(ctx, id)
	if err != nil {
		return nil, err
	}
	var claimed map[string]bool
	if a, ok := h.getActor(id); ok {
		a.mu.Lock()
		claimed = a.claimed
		a.mu.Unlock()
	}
	out := []store.InboxItem{}
	for _, it := range items {
		if it.Kind == store.InboxQueued && !claimed[it.ItemID] {
			out = append(out, it)
		}
	}
	return out, nil
}

// Unqueue 取消一条排队的输入。返回 false = 没有这条、已开始处理或不是排队项。
func (h *Host) Unqueue(ctx context.Context, id, itemID string) (bool, error) {
	qs, ok := h.DB.(store.QueueStore)
	if !ok {
		return false, ErrQueueUnsupported
	}
	a, open := h.getActor(id)
	if open {
		a.mu.Lock()
		if a.claimed[itemID] {
			a.mu.Unlock()
			return false, nil
		}
	}
	done, err := qs.Unqueue(ctx, id, itemID)
	if open {
		a.mu.Unlock()
	}
	if err != nil || !done {
		return false, err
	}
	h.publishQueue(id)
	return true, nil
}

// publishQueue 发布当前排队列表（界面据此刷新排队区）。读失败时不发。
func (h *Host) publishQueue(id string) {
	if h.Bus == nil {
		return
	}
	q, err := h.Queued(context.Background(), id)
	if err != nil {
		return
	}
	h.publish(id, EventQueueChanged, map[string]any{"queued": q})
}

// queuedIDs 一批信箱项里的排队项 id。
func queuedIDs(batch []store.InboxItem) []string {
	var out []string
	for _, it := range batch {
		if it.Kind == store.InboxQueued {
			out = append(out, it.ItemID)
		}
	}
	return out
}

// claimQueued 在 actor.mu 内核对排队项仍未消费（没被取消）并登记为已认领。任何一条已不在信箱返回 false。
func (a *actor) claimQueued(ctx context.Context, ids []string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, id := range ids {
		in, err := a.h.DB.InInbox(ctx, a.id, id)
		if err != nil {
			return false, err
		}
		if !in {
			return false, nil
		}
	}
	a.claimed = inboxSet(ids)
	return true, nil
}

// collectDrainBatch 回合间取下一批：按准入顺序找第一个 input 或 queued。
// 排队项单独成一批（带上它前面未处理的旧格式 note）；input 按 collectInboxBatch 合并，
// 但不越过其后的排队项（排队项之后到达的信件不能抢在它前面）。
func collectDrainBatch(items []store.InboxItem, skip map[string]bool) []store.InboxItem {
	first := -1
	for i, it := range items {
		if skip[it.ItemID] {
			continue
		}
		if it.Kind == store.InboxInput || it.Kind == store.InboxQueued {
			first = i
			break
		}
	}
	if first < 0 {
		return nil
	}
	if items[first].Kind == store.InboxQueued {
		var out []store.InboxItem
		for _, it := range items[:first] {
			if !skip[it.ItemID] && it.Kind == store.InboxNote {
				out = append(out, it)
			}
		}
		return append(out, items[first])
	}
	end := len(items)
	for i := first + 1; i < len(items); i++ {
		if !skip[items[i].ItemID] && items[i].Kind == store.InboxQueued {
			end = i
			break
		}
	}
	return collectInboxBatch(items[:end], skip)
}
