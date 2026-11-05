package appapi

// 交互命令（protocol 2，docs/06 - 应用协议契约.md 10.6）：interaction.respond、interactions.list、快照里的 interactions。
//
// 规则：回答者由连接身份决定（不在 responders 里返回 forbidden）；只收第一条回答，之后返回 conflict（answered，
// details.outcome 带已有结果），同一 client_id 的重发返回第一次的结果；会话由别的进程持有时返回 conflict（leased）。
// scope=session 的批准按调用生成会话规则，持久化到顶层会话头异步进行。

import (
	"context"
	"encoding/json"
	"fmt"

	"ybase/appapi/wire"
	"ybase/runtime/host"
	"ybase/runtime/store"
)

// Respond 以本机身份回答交互（进程内调用；连接上的调用经 Conn.Call 带连接身份）。
func (v *App) Respond(ctx context.Context, in wire.RespondArgs) (wire.RespondResult, error) {
	return v.respondAs(ctx, in, LocalPrincipal)
}

func (v *App) respondAs(ctx context.Context, in wire.RespondArgs, p wire.Principal) (wire.RespondResult, error) {
	if in.Interaction == "" {
		return wire.RespondResult{}, Errorf(wire.CodeBadRequest, "缺少 interaction")
	}
	if len(in.ClientID) > 128 {
		return wire.RespondResult{}, invalid("client_id 超过 128 字节")
	}
	db := v.o.DB
	cur, err := db.Interaction(ctx, in.Interaction)
	if err != nil {
		return wire.RespondResult{}, err
	}
	it := v.wireInteraction(cur)
	if !contains(it.Responders, p.ID) {
		return wire.RespondResult{}, Errorf(wire.CodeForbidden, "身份 %s 不能回答交互 %s", p.ID, in.Interaction)
	}
	if cur.Outcome != "" {
		return answered(cur, in.ClientID)
	}
	res := store.Resolution{By: p.ID, ClientID: in.ClientID}
	var ans host.Answer
	persist := func() {}
	switch cur.Kind {
	case wire.InteractionApproval:
		if in.Response.Approve == nil {
			return wire.RespondResult{}, Errorf(wire.CodeBadRequest, "approval 交互须带 response.approve")
		}
		ans.Approve = *in.Response.Approve
		res.Outcome = wire.OutcomeDenied
		if ans.Approve {
			res.Outcome = wire.OutcomeApproved
		}
		switch in.Response.Scope {
		case "", "once":
		case "session":
			if !ans.Approve {
				return wire.RespondResult{}, invalid("scope=session 只用于批准")
			}
			if v.o.Modes == nil {
				return wire.RespondResult{}, Errorf(wire.CodeUnsupported, "未装权限插件，不能生成会话规则")
			}
			persist = func() { v.allowForSession(cur.SessionID, it.Approval) }
		default:
			return wire.RespondResult{}, invalid("scope 只能是 once / session，得到 %q", in.Response.Scope)
		}
	case wire.InteractionQuestion:
		if in.Response.Text == nil {
			return wire.RespondResult{}, Errorf(wire.CodeBadRequest, "question 交互须带 response.text")
		}
		ans.Text, res.Answer, res.Outcome = *in.Response.Text, *in.Response.Text, wire.OutcomeAnswered
	default:
		return wire.RespondResult{}, Errorf(wire.CodeUnsupported, "不支持的交互种类 %q", cur.Kind)
	}
	got, won, err := v.o.Host.Respond(ctx, in.Interaction, res, ans)
	if err != nil {
		return wire.RespondResult{}, err
	}
	if !won {
		return answered(got, in.ClientID)
	}
	persist() // 生效后才生成会话规则：输掉的回答不留副作用
	return wire.RespondResult{Outcome: got.Outcome}, nil
}

// answered 交互已结束：同一 client_id 的重发返回第一次的结果，否则 conflict（answered）。
func answered(p store.Pending, clientID string) (wire.RespondResult, error) {
	if clientID != "" && p.ClientID == clientID {
		return wire.RespondResult{Outcome: p.Outcome}, nil
	}
	e := conflict(wire.ConflictAnswered, "交互 %s 已结束（%s）", p.InteractionID, p.Outcome)
	e.Details["outcome"] = p.Outcome
	return wire.RespondResult{}, e
}

// allowForSession scope=session：内存立即生效，持久化到顶层会话头异步进行（回合中 SetExt 要等步边界，
// 而这一步正等着本次确认，同步等会死锁）；失败发 notice。
func (v *App) allowForSession(sessionID string, ap *wire.Approval) {
	if ap == nil {
		return
	}
	m := v.o.Modes
	_, all := m.AllowForSession(sessionID, ap.Tool, ap.Args)
	_, key := m.ExtKeys()
	top := topOf(sessionID)
	hd, ok := v.o.Host.Get(top)
	if !ok {
		return
	}
	go func() {
		if err := hd.Do(host.Command{Kind: host.SetExt, Key: key, Value: all}); err != nil {
			v.Notice(sessionID, fmt.Sprintf("[会话规则未能保存：%v]", err))
		}
	}()
}

// Interactions 当前身份可以回答的全部未结束交互（跨会话）。
func (v *App) Interactions(ctx context.Context, _ wire.Empty) (wire.InteractionsResult, error) {
	return v.interactionsFor(ctx, LocalPrincipal)
}

func (v *App) interactionsFor(ctx context.Context, p wire.Principal) (wire.InteractionsResult, error) {
	ps, err := v.o.DB.OpenInteractions(ctx, "")
	if err != nil {
		return wire.InteractionsResult{}, err
	}
	out := wire.InteractionsResult{Items: []wire.Interaction{}}
	for _, q := range ps {
		if it := v.wireInteraction(q); contains(it.Responders, p.ID) {
			out.Items = append(out.Items, it)
		}
	}
	return out, nil
}

// openInteractions 快照用：会话自己的与以它为根的子会话的未结束交互（只读已提交数据）。
func (v *App) openInteractions(ctx context.Context, id string) []wire.Interaction {
	out := []wire.Interaction{}
	ps, err := v.o.DB.OpenInteractions(ctx, id)
	if err != nil {
		return out
	}
	for _, p := range ps {
		out = append(out, v.wireInteraction(p))
	}
	return out
}

// wireInteraction 登记行 → 线上交互。载荷是宿主登记时生成的线上形状；approval 补上"本次会话内都允许"会生成的规则。
func (v *App) wireInteraction(p store.Pending) wire.Interaction {
	var it wire.Interaction
	_ = json.Unmarshal(p.Payload, &it)
	it.ID, it.Session, it.Kind = p.InteractionID, p.SessionID, p.Kind
	if it.Responders == nil {
		it.Responders = []string{}
	}
	if it.OpenedAt == 0 {
		it.OpenedAt = p.CreatedAt
	}
	if ap := it.Approval; ap != nil && v.o.Modes != nil {
		ap.SessionRules, ap.Blocked = v.o.Modes.SessionAllow(ap.Tool, ap.Args)
	}
	return it
}

func contains(xs []string, x string) bool {
	for _, s := range xs {
		if s == x {
			return true
		}
	}
	return false
}
