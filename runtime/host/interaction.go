package host

// 交互（protocol 2，docs/06 - 应用协议契约.md 10.6）：工具确认与模块提问都登记成交互（store.InteractionStore），
// 打开与结束都是持久事件；交互 id 公开，corr 只用于回信路径、不推给订阅者。
//
// 结束的路径与结果：
//   - interaction.respond（Host.Respond）：先在存储里结束（只收第一条），赢了再交给等待方；
//   - 旧路径（Confirm 命令按 call_id、回信信封按 corr）：等待方拿到答案后自己结束，结果 approved / denied；
//   - 回合取消、actor 退出：voided；过了截止：expired；
//   - 进程重启：上次进程留下的未结束交互在会话打开时标为 voided（等待它的工具调用会按工具记录恢复或重问）。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"tenon/runtime/mail"
	"tenon/runtime/store"
)

// 交互结果（与线上 outcome 同值）。
const (
	OutcomeApproved = "approved"
	OutcomeDenied   = "denied"
	OutcomeAnswered = "answered"
	OutcomeExpired  = "expired"
	OutcomeVoided   = "voided"
)

// Answer 交给等待方的回答：approval / confirm 用 Approve，question 用 Text，choice / form 等用 Value（JSON）。
type Answer struct {
	Approve bool
	Text    string
	Value   json.RawMessage
}

// Request 一次非工具交互（Host.Interact）：Kind 与 Body 原样进线上 Interaction（Body 放在与 Kind 同名的键下），
// Schema 是回答须满足的 JSON Schema（可空）；Blocking 为假时界面内联显示、不接管输入框。Deadline 零值 = 不限。
type Request struct {
	Kind     string
	Title    string
	Body     map[string]any
	Schema   json.RawMessage
	Blocking *bool
	Deadline time.Time
}

// jsonArgs 工具参数作为 JSON 值放进载荷；不合法的 JSON 包成字符串（不让整个载荷编码失败）。
func jsonArgs(raw json.RawMessage) any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	if json.Valid(raw) {
		return raw
	}
	return string(raw)
}

// rootOf 子会话（"<顶层>/<子>"）的顶层会话 id；顶层会话返回空。
func rootOf(id string) string {
	if i := strings.Index(id, "/"); i > 0 {
		return id[:i]
	}
	return ""
}

// interactionPayload interaction.opened 的载荷（线上 Interaction 的形状）；不含 corr。extra 是额外的顶层键（schema、blocking）。
func interactionPayload(id, session string, turn uint32, kind, title string, body map[string]any,
	responders []string, deadline time.Time, extra map[string]any) json.RawMessage {
	m := map[string]any{"id": id, "session": session, "turn": turn, "kind": kind, "title": title,
		"responders": responders, "opened_at": time.Now().UnixMilli()}
	if !deadline.IsZero() {
		m["deadline"] = deadline.UnixMilli()
	}
	for k, v := range extra {
		m[k] = v
	}
	m[kind] = body
	b, _ := json.Marshal(m)
	return b
}

// responders 谁能回答本会话的交互：会话创建者；本机会话为 local。
func (a *actor) responders() []string {
	if a.ag.Ctx != nil {
		if c := a.ag.Ctx.Session().Creator; c != "" {
			return []string{c}
		}
	}
	return []string{"local"}
}

// openInteraction 登记一次交互（同时是一条待回复）。登记失败时退回只登记 corr（回信路径仍可用），
// 再失败 corr 为空。返回交互 id（空 = 没登记成交互）与 corr。
func (a *actor) openInteraction(kind, title string, body map[string]any, deadline time.Time, extra ...map[string]any) (iid, corr string) {
	corr = mail.NewID()
	iid = "i-" + NewItemID()
	idx, turn := 0, uint32(0)
	if core := a.ag.Core; core != nil {
		c := core.Context()
		idx = c.Base + len(c.Items)
		turn = core.TurnNumber()
	}
	var dl int64
	if !deadline.IsZero() {
		dl = deadline.UnixMilli()
	}
	p := store.Pending{Corr: corr, SessionID: a.id, Trace: a.ag.TraceID(), IssuedIdx: idx, Deadline: dl,
		InteractionID: iid, Kind: kind, Root: rootOf(a.id),
		Payload: interactionPayload(iid, a.id, turn, kind, title, body, a.responders(), deadline, firstMap(extra))}
	if err := a.h.DB.OpenInteraction(context.Background(), p, a.epoch); err == nil {
		return iid, corr
	}
	p.InteractionID, p.Kind, p.Payload, p.Root = "", "", nil, ""
	if err := a.h.DB.IssueCorr(context.Background(), p); err != nil {
		return "", ""
	}
	return "", corr
}

func firstMap(ms []map[string]any) map[string]any {
	if len(ms) == 0 {
		return nil
	}
	return ms[0]
}

// closeInteraction 等待结束后收尾：结束交互（已被 Respond 结束的不变），或只作废 corr。
func (a *actor) closeInteraction(iid, corr, outcome string) {
	ctx := context.Background()
	if iid != "" && outcome == "" { // 已由 Respond 结束
		return
	}
	if iid != "" {
		_, _, _ = a.h.DB.ResolveInteraction(ctx, iid, store.Resolution{Outcome: outcome})
		return
	}
	if corr != "" {
		_ = a.h.DB.VoidCorr(ctx, corr)
	}
}

// waitOutcome 等待因 ctx 结束时的结果：过了截止为 expired，否则 voided。
func waitOutcome(deadline time.Time) string {
	if !deadline.IsZero() && !time.Now().Before(deadline) {
		return OutcomeExpired
	}
	return OutcomeVoided
}

// Respond 回答一次交互（protocol 2 interaction.respond）：在存储里结束它（只收第一条），赢了再交给等待方。
// 没赢（已结束）返回已有的结束内容与 won = false。等待方不在本进程：会话由别的进程持有时返回 store.ErrLeaseLost；
// 会话在本进程但没人等（上次进程留下的），把它标为 voided 后返回 won = false。
func (h *Host) Respond(ctx context.Context, id string, r store.Resolution, ans Answer) (store.Pending, bool, error) {
	p, err := h.DB.Interaction(ctx, id)
	if err != nil {
		return store.Pending{}, false, err
	}
	if p.Outcome != "" {
		return p, false, nil
	}
	h.humanMu.Lock()
	ch, okA := h.humanWait[p.Corr]
	qch, okQ := h.askWait[p.Corr]
	h.humanMu.Unlock()
	if !okA && !okQ {
		if _, open := h.Get(p.SessionID); !open {
			return store.Pending{}, false, fmt.Errorf("%w: 会话 %s 不在本进程，交互 %s 由持有它的进程处理", store.ErrLeaseLost, p.SessionID, id)
		}
		got, _, err := h.DB.ResolveInteraction(ctx, id, store.Resolution{Outcome: OutcomeVoided})
		return got, false, err
	}
	got, won, err := h.DB.ResolveInteraction(ctx, id, r)
	if err != nil || !won {
		return got, won, err
	}
	if okA {
		select {
		case ch <- ans.Approve:
		default:
		}
	} else {
		select {
		case qch <- ans:
		default:
		}
	}
	return got, true, nil
}

// Ask 在已打开的会话上向人提问（question 交互）并等回答。opened 非空时在登记后、等待前调用（protocol 1 的 ask 事件用）。
// ctx 结束返回 ctx.Err()，会话退出返回 ErrClosed，交互随之 voided。
func (h *Host) Ask(ctx context.Context, sessionID, question string, options []string, opened func(interactionID string)) (string, error) {
	body := map[string]any{"text": question}
	if len(options) > 0 {
		body["options"] = options
	}
	ans, err := h.interact(ctx, sessionID, Request{Kind: "question", Title: question, Body: body}, opened)
	return ans.Text, err
}

// Interact 在已打开的会话上发起一次非工具交互（choice / form / confirm 或插件自定义的 kind），等第一条回答。
// 回答经 interaction.respond 到达（appapi 按 kind 校验）；ctx 结束、会话退出时交互 voided，过了 Deadline 为 expired。
// 工具确认（approval）不走这里：它由工具调用的确认路径登记。
func (h *Host) Interact(ctx context.Context, sessionID string, r Request) (Answer, error) {
	if r.Kind == "" || r.Kind == "approval" {
		return Answer{}, fmt.Errorf("host: Interact 不接受 kind %q", r.Kind)
	}
	return h.interact(ctx, sessionID, r, nil)
}

func (h *Host) interact(ctx context.Context, sessionID string, r Request, opened func(string)) (Answer, error) {
	hd, ok := h.Get(sessionID)
	if !ok {
		return Answer{}, fmt.Errorf("%w: %s", ErrSessionNotFound, sessionID)
	}
	a := hd.a
	extra := map[string]any{}
	if len(r.Schema) > 0 {
		if !json.Valid(r.Schema) {
			return Answer{}, fmt.Errorf("host: 交互的 schema 不是合法 JSON")
		}
		extra["schema"] = r.Schema
	}
	if r.Blocking != nil {
		extra["blocking"] = *r.Blocking
	}
	body := r.Body
	if body == nil {
		body = map[string]any{}
	}
	iid, corr := a.openInteraction(r.Kind, r.Title, body, r.Deadline, extra)
	if corr == "" || iid == "" {
		if corr != "" {
			_ = h.DB.VoidCorr(context.Background(), corr)
		}
		return Answer{}, fmt.Errorf("host: 交互登记失败")
	}
	ch := make(chan Answer, 1)
	h.humanMu.Lock()
	if h.askWait == nil {
		h.askWait = map[string]chan Answer{}
	}
	h.askWait[corr] = ch
	h.humanMu.Unlock()
	outcome := OutcomeVoided
	defer func() {
		h.humanMu.Lock()
		delete(h.askWait, corr)
		h.humanMu.Unlock()
		a.closeInteraction(iid, corr, outcome)
	}()
	if opened != nil {
		opened(iid)
	}
	var timeout <-chan time.Time
	if !r.Deadline.IsZero() {
		t := time.NewTimer(time.Until(r.Deadline))
		defer t.Stop()
		timeout = t.C
	}
	select {
	case ans := <-ch:
		outcome = "" // 已由 Respond 结束（结果按回答定），closeInteraction 不覆盖
		return ans, nil
	case <-timeout:
		outcome = OutcomeExpired
		return Answer{}, fmt.Errorf("host: 交互 %s 已过截止时间", iid)
	case <-ctx.Done():
		outcome = waitOutcome(r.Deadline)
		return Answer{}, ctx.Err()
	case <-a.exit:
		return Answer{}, ErrClosed
	}
}

// voidStale 会话打开时：上次进程留下的、本会话发起的未结束交互标为 voided（等它的人已经不在了）。
func (h *Host) voidStale(ctx context.Context, id string) {
	ps, err := h.DB.OpenInteractions(ctx, id)
	if err != nil {
		return
	}
	for _, p := range ps {
		if p.SessionID == id {
			_, _, _ = h.DB.ResolveInteraction(ctx, p.InteractionID, store.Resolution{Outcome: OutcomeVoided})
		}
	}
}
