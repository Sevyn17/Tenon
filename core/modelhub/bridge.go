package modelhub

// 桥接（M3.13）：把 Hub 包装成宿主循环用的单次调用接口。
// 会话缓存亲和 id（D5）每会话一个、持久不变，由宿主传入；need id 随每次调用传入（R3）。

import (
	"context"
	"errors"

	"ybase-kernel/core/session"
)

// Session 一个会话对 Hub 的调用视图。
type Session struct {
	Hub        *Hub
	SessionKey string // 会话缓存亲和 id：重试、压缩、换模型不变；fork 换新
}

// StreamRequest 发起一次会话调用，增量文本经 onText 送出；Reset 时 onReset 被调用（界面撤掉已显示的半截）。
// 失败时返回已收到的部分回复（中断时宿主写 interrupted 条目）与 *Failure。
func (s *Session) StreamRequest(ctx context.Context, requestID string, req session.Request,
	onText func(string), onReset func()) (session.AssistantItem, session.StopReason, error) {
	r := &Request{Req: req, RequestID: requestID, SessionKey: s.SessionKey}
	var (
		done *Done
		fail *Failure
	)
	for ev := range s.Hub.Call(ctx, r) {
		switch {
		case ev.Reset:
			if onReset != nil {
				onReset()
			}
		case ev.TextDelta != "":
			if onText != nil {
				onText(ev.TextDelta)
			}
		case ev.Done != nil:
			done = ev.Done
		case ev.Failed != nil:
			fail = ev.Failed
		}
	}
	if done != nil {
		return done.Reply, done.Stop, nil
	}
	if fail == nil {
		fail = &Failure{Code: CodeProtocol, Message: "调用未产生终止事件"}
	}
	var partial session.AssistantItem
	if fail.Partial != nil {
		partial = *fail.Partial
	}
	return partial, session.StopReason{}, fail
}

// Stream 无 need id 的调用（兼容旧接口；宿主循环优先走 StreamRequest）。
func (s *Session) Stream(ctx context.Context, req session.Request, onText func(string)) (session.AssistantItem, session.StopReason, error) {
	return s.StreamRequest(ctx, "adhoc-"+newID(), req, onText, nil)
}

// IsCode 判断 err 是否为某稳定失败码。
func IsCode(err error, c Code) bool {
	var f *Failure
	return errors.As(err, &f) && f.Code == c
}
