package appapi

// trace 渲染（08 8.7）：纯展示，从 runtime/host 移来（2026-11-04 审查 P2 归属：与宿主无关）。

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Sevyn17/tenon/runtime/store"
)

// RenderTrace 渲染一次任务的路径：经过的会话、信封、工具与拒绝原因。协议命令 trace.explain 用它，应用的命令行也可以直接调。
func RenderTrace(ctx context.Context, db store.TraceStore, trace string) (string, error) {
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
		label := traceLabels[ev.Kind]
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

var traceLabels = map[string]string{"envelope": "信封", "session": "会话", "tool": "工具", "guard_denied": "拒绝"}
