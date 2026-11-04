package appapi

// 待确认调用的记录：App 订阅 human_requested，记下 call_id → 工具名 / 参数，
// 供 session.confirm(decision=session) 生成会话规则（客户端不必回传工具与参数）。

import (
	"context"
	"fmt"
	"sync"

	"ybase/runtime/bus"
)

type pendingCall struct {
	tool string
	args string
}

type pendingSet struct {
	mu sync.Mutex
	m  map[string]pendingCall
}

// watchPending 启动记录协程；ctx 结束（或总线关闭）时退出。
func (a *App) watchPending(ctx context.Context) {
	a.pend.m = map[string]pendingCall{}
	sub := a.o.Bus.Subscribe(ctx, bus.Filter{Kinds: []string{EventHumanRequested}})
	go func() {
		for ev := range sub.C() {
			d, ok := ev.Data.(map[string]any)
			if !ok {
				continue
			}
			id, _ := d["call_id"].(string)
			if id == "" {
				continue
			}
			tool, _ := d["tool"].(string)
			args := ""
			if v, ok := d["args"]; ok && v != nil {
				if s, ok := v.(string); ok {
					args = s
				} else {
					args = fmt.Sprint(v)
				}
			}
			a.pend.mu.Lock()
			a.pend.m[id] = pendingCall{tool: tool, args: args}
			a.pend.mu.Unlock()
		}
	}()
}

// takePending 取出并删除 call_id 的记录；没有返回零值。
func (a *App) takePending(callID string) pendingCall {
	a.pend.mu.Lock()
	defer a.pend.mu.Unlock()
	p := a.pend.m[callID]
	delete(a.pend.m, callID)
	return p
}
