package host

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"ybase-kernel/core/store"
)

// recordTrace 记一条 trace 事件（08 8.7）：记录失败不影响投递本身。
func (h *Host) recordTrace(trace, session, kind, detail string) {
	if trace == "" || h.DB == nil {
		return
	}
	_ = h.DB.RecordTrace(context.Background(), store.TraceEvent{Trace: trace, Session: session, Kind: kind, Detail: detail})
}

// ExplainTrace 渲染一次任务的路径：经过的会话、信封、工具与拒绝原因（08 8.7）。
func ExplainTrace(ctx context.Context, db store.Store, trace string) (string, error) {
	evs, err := db.TraceEvents(ctx, trace)
	if err != nil {
		return "", err
	}
	if len(evs) == 0 {
		return fmt.Sprintf("trace %s：没有记录\n", trace), nil
	}
	sort.SliceStable(evs, func(i, j int) bool { return evs[i].At < evs[j].At })
	seen := map[string]bool{}
	sessions := make([]string, 0, 4)
	for _, ev := range evs {
		if ev.Session != "" && !seen[ev.Session] {
			seen[ev.Session] = true
			sessions = append(sessions, ev.Session)
		}
	}
	sort.Strings(sessions)
	var b strings.Builder
	fmt.Fprintf(&b, "trace %s\n", trace)
	if len(sessions) > 0 {
		fmt.Fprintf(&b, "会话：%s\n", strings.Join(sessions, "、"))
	}
	for _, ev := range evs {
		label := map[string]string{
			"envelope":     "信封",
			"session":      "会话",
			"tool":         "工具",
			"guard_denied": "拒绝",
		}[ev.Kind]
		if label == "" {
			label = ev.Kind
		}
		sid := ev.Session
		if sid == "" {
			sid = "-"
		}
		fmt.Fprintf(&b, "  %-4s %s  %s\n", label, sid, ev.Detail)
	}
	return b.String(), nil
}
