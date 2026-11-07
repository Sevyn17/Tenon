package host

import (
	"context"

	"tenon/runtime/store"
)

// recordTrace 记一条 trace 事件（08 8.7）：记录失败不影响投递本身。渲染在 appapi.RenderTrace。
func (h *Host) recordTrace(trace, session, kind, detail string) {
	if trace == "" || h.DB == nil {
		return
	}
	_ = h.DB.RecordTrace(context.Background(), store.TraceEvent{Trace: trace, Session: session, Kind: kind, Detail: detail})
}
