package appapi

// 人工确认的回信应答（11 11.2 human: 地址）与 explain trace（08 8.7）。
//
// 内核为每次确认签发 corr（human_requested 事件的 corr 字段）；界面可以按 call_id 走
// session.confirm（兼容旧界面、能生成「本次会话内都允许」规则），也可以按 corr 回信——
// 回信走信封投递（KindReply + corr），经联系名单与 corr 核对，只收第一条。

import (
	"context"

	"ybase-kernel/core/host"
	"ybase-kernel/core/mail"
)

// ReplyArgs session.reply 参数：按 corr 回答一次人工确认。
type ReplyArgs struct {
	Session string `json:"session"`
	Corr    string `json:"corr"`
	// User 回答者（缺省 "local"，单机本人）；必须是会话创建者（联系名单）。
	User string `json:"user,omitempty"`
	// Approve 批准与否；Body 非空时以 Body 为准（"approve" / "deny" / JSON）。
	Approve bool   `json:"approve"`
	Body    string `json:"body,omitempty"`
}

// Reply 以回信信封回答人工确认（corr 来自 human_requested 事件）。
func (a *App) Reply(ctx context.Context, in ReplyArgs) (Empty, error) {
	if err := need(in.Session); err != nil {
		return Empty{}, err
	}
	if in.Corr == "" {
		return Empty{}, Errorf(CodeBadRequest, "缺少 corr")
	}
	user := in.User
	if user == "" {
		user = "local"
	}
	body := in.Body
	if body == "" {
		body = "deny"
		if in.Approve {
			body = "approve"
		}
	}
	err := a.o.Host.Deliver(ctx, mail.Envelope{
		From: mail.Address{Kind: mail.AddrHuman, ID: user},
		To:   mail.Address{Kind: mail.AddrSession, ID: in.Session},
		Kind: mail.KindReply, Corr: in.Corr, Body: body,
	})
	return Empty{}, wrap(err)
}

// TraceArgs trace.explain 参数。
type TraceArgs struct {
	Trace string `json:"trace"`
}

// TraceResult trace.explain 结果：渲染好的文本（与 CLI `ybase explain trace <id>` 同格式）。
type TraceResult struct {
	Text string `json:"text"`
}

// ExplainTrace 列出一次任务经过的会话、信封、工具与拒绝原因。
func (a *App) ExplainTrace(ctx context.Context, in TraceArgs) (TraceResult, error) {
	if in.Trace == "" {
		return TraceResult{}, Errorf(CodeBadRequest, "缺少 trace")
	}
	out, err := host.ExplainTrace(ctx, a.o.DB, in.Trace)
	if err != nil {
		return TraceResult{}, wrap(err)
	}
	return TraceResult{Text: out}, nil
}
