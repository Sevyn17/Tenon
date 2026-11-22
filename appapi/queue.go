package appapi

// 排队发送（客户端改进 M12 协议T20）：session.send {queue}、session.unqueue、快照 queued 与 queue.changed。

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/runtime/host"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
)

// 本文件的命令（按文件登记，v2.go 的 registerCommands）。
func init() {
	registerCommands(map[string]handler{
		"session.unqueue": bind((*App).Unqueue),
	})
}

// CapSendQueue hello 能力：存储支持排队（实现了 store.QueueStore）。
const CapSendQueue = "send.queue"

// queueOK 宿主与存储都支持排队。
func (v *App) queueOK() bool {
	return v.o.Host != nil && v.o.Host.QueueSupported()
}

// queuedOf 快照里的排队列表；不支持或没有时给 nil（字段省略）。
func (v *App) queuedOf(ctx context.Context, id string) []wire.QueuedInput {
	if !v.queueOK() {
		return nil
	}
	items, err := v.o.Host.Queued(ctx, id)
	if err != nil || len(items) == 0 {
		return nil
	}
	return wireQueued(items)
}

// wireQueued 信箱项 → 线上排队项。
func wireQueued(items []store.InboxItem) []wire.QueuedInput {
	out := make([]wire.QueuedInput, 0, len(items))
	for _, it := range items {
		var c session.Content
		_ = json.Unmarshal([]byte(it.Input), &c)
		parts := wireParts(c)
		if parts == nil {
			parts = []wire.Part{}
		}
		out = append(out, wire.QueuedInput{ID: it.ItemID, Content: parts, At: it.At.UnixMilli()})
	}
	return out
}

// Unqueue 取消一条排队的输入。存储不支持返回 unsupported；没有这条、已开始处理或已取消时 removed=false。
func (v *App) Unqueue(ctx context.Context, in wire.UnqueueArgs) (wire.UnqueueResult, error) {
	if err := need(in.Session); err != nil {
		return wire.UnqueueResult{}, err
	}
	if in.ID == "" {
		return wire.UnqueueResult{}, Errorf(wire.CodeBadRequest, "missing id")
	}
	if !v.queueOK() {
		return wire.UnqueueResult{}, Errorf(wire.CodeUnsupported, "store does not support queueing")
	}
	ok, err := v.o.Host.Unqueue(ctx, in.Session, in.ID)
	if errors.Is(err, host.ErrQueueUnsupported) {
		return wire.UnqueueResult{}, Errorf(wire.CodeUnsupported, "store does not support queueing")
	}
	if err != nil {
		return wire.UnqueueResult{}, err
	}
	return wire.UnqueueResult{Removed: ok}, nil
}

// queueChanged 总线上的 queue_changed → 线上 queue.changed。
func queueChanged(m map[string]any) wire.QueueChanged {
	out := wire.QueueChanged{Queued: []wire.QueuedInput{}}
	switch q := m["queued"].(type) {
	case []store.InboxItem:
		out.Queued = wireQueued(q)
	case nil:
	default: // 经 JSON 往返过（不应发生）：按 InboxItem 解回
		var items []store.InboxItem
		if raw, err := json.Marshal(q); err == nil && json.Unmarshal(raw, &items) == nil {
			out.Queued = wireQueued(items)
		}
	}
	return out
}
