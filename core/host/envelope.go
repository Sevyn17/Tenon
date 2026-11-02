package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"ybase-kernel/core/agent"
	"ybase-kernel/core/mail"
	"ybase-kernel/core/plugin"
	"ybase-kernel/core/session"
	"ybase-kernel/core/store"
)

// ErrSessionNotFound 会话地址只投不建（13 13.3：查不到就拒绝，不自动新建）。
var ErrSessionNotFound = errors.New("host: 会话不存在")

// ErrTooManyHops 信封跳数超过上限（08 D4：跳数超限截断回错）。
var ErrTooManyHops = errors.New("host: 信封跳数超过上限")

// ErrNotAllowed 发送方不在目标会话的联系名单里（08 D4）。
var ErrNotAllowed = errors.New("host: 发送方不在联系名单里")

// MaxHop 单封信封允许的最大跳数。
const MaxHop = 8

// Deliver 投递一封信封（13 13.3）：session: 只查不建；agent: 带 key 时查或建
// （B19）；human: 等分片二后半。
//
// 返回前信封已落 inbox（幂等：同 id 只算一次）；回合中到达的 wake 会在下一次调模型前合并插入。
func (h *Host) Deliver(ctx context.Context, env mail.Envelope) error {
	if err := env.Normalize(); err != nil {
		return err
	}
	if env.Trace == "" { // 一次任务共享 trace id（08 8.7）：缺省由内核分配，调用方传值忽略。
		env.Trace = mail.NewID()
	}
	// 跳数（08 D4）：每投递一次加一，超过上限截断并记 guard.denied。
	env.Hop++
	if env.Hop > MaxHop {
		h.recordTrace(env.Trace, env.To.ID, "guard_denied", "hop")
		h.publish(env.To.ID, "guard.denied", map[string]any{"from": env.From.String(), "to": env.To.String(), "reason": "hop"})
		return fmt.Errorf("%w: %d > %d", ErrTooManyHops, env.Hop, MaxHop)
	}
	switch env.To.Kind {
	case mail.AddrSession:
		return h.deliverSession(ctx, env)
	case mail.AddrAgent:
		return h.deliverAgent(ctx, env)
	default:
		return fmt.Errorf("host: 暂不支持投递到 %s（后一分片提供）", env.To.String())
	}
}

// deliverAgent 投给 agent:<定义名>：带 key 时按（定义, 创建者, key）查或建会话（B19）。
func (h *Host) deliverAgent(ctx context.Context, env mail.Envelope) error {
	creator := ""
	if env.From.Kind == mail.AddrHuman {
		creator = env.From.ID
	}
	if env.Key != "" {
		id, err := h.DB.FindSessionByKey(ctx, env.To.ID, creator, env.Key)
		if err == nil {
			env.To = mail.Address{Kind: mail.AddrSession, ID: id}
			return h.deliverSession(ctx, env)
		}
		if !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	// 新建会话前守卫（08 8.1）：可联系名单、所属树深度等由组件规则表达。
	if v, why := h.evalGuard(plugin.GuardRequest{Point: plugin.GuardSessionOpen, Envelope: &env, Agent: env.To.ID}); v == plugin.Deny {
		h.recordTrace(env.Trace, "", "guard_denied", why)
		h.publish("", "guard.denied", map[string]any{"from": env.From.String(), "to": env.To.String(), "agent": env.To.ID, "reason": why})
		return fmt.Errorf("%w: %s", ErrNotAllowed, why)
	}
	id := "ag-" + NewItemID()
	meta := store.SessionMeta{ID: id, AgentID: env.To.ID, Creator: creator, SessionKey: env.Key, Origin: "agent"}
	pre := func(ag *agent.Agent, cfg *session.Config) error {
		if ag.Def != nil {
			meta.AgentID, meta.AgentHash = ag.Def.ID, ag.Def.Version
		}
		_, err := ag.OpenAs(ctx, cfg, meta)
		return err
	}
	if _, err := h.open(ctx, id, env.To.ID, h.New, pre); err != nil {
		// 并发建同键：唯一约束冲突后重查一次再投（13 13.3 B19）。
		if env.Key != "" && errors.Is(err, store.ErrConflict) {
			if sid, ferr := h.DB.FindSessionByKey(ctx, env.To.ID, creator, env.Key); ferr == nil {
				env.To = mail.Address{Kind: mail.AddrSession, ID: sid}
				return h.deliverSession(ctx, env)
			}
		}
		return err
	}
	env.To = mail.Address{Kind: mail.AddrSession, ID: id}
	return h.deliverSession(ctx, env)
}

// DeliverText 是投递一条用户文本的便捷入口（等价于 from=human 的 wake 请求）。
func (h *Host) DeliverText(ctx context.Context, sessionID, from, text string) error {
	return h.Deliver(ctx, mail.Envelope{
		From:   mail.Address{Kind: mail.AddrHuman, ID: from},
		To:     mail.Address{Kind: mail.AddrSession, ID: sessionID},
		Kind:   mail.KindRequest,
		Origin: mail.OriginHuman,
		Body:   text,
	})
}

func (h *Host) deliverSession(ctx context.Context, env mail.Envelope) error {
	id := env.To.ID
	ok, err := h.DB.Exists(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s", ErrSessionNotFound, id)
	}
	if st, err := h.DB.SessionStatus(ctx, id); err == nil && st == "deleted" {
		h.publish(id, "mail.rejected", map[string]any{"from": env.From.String(), "reason": "closed"})
		return fmt.Errorf("%w: %s", ErrClosed, id)
	}
	// 联系名单（08 D4）：默认只放开创建者本人、登记组件、以及带 corr 的回信；
	// 组件登记的 Allow 规则只能放开这条「往 session: 投递」的缺省限制（D4）。
	verdict, reason := h.evalGuard(plugin.GuardRequest{Point: plugin.GuardDeliver, Envelope: &env})
	if verdict == plugin.Deny {
		h.recordTrace(env.Trace, id, "guard_denied", reason)
		h.publish(id, "guard.denied", map[string]any{"from": env.From.String(), "to": env.To.String(), "reason": reason})
		return fmt.Errorf("%w: %s", ErrNotAllowed, reason)
	}
	l, err := h.DB.Load(ctx, id, nil)
	if err != nil {
		return err
	}
	if err := guardContact(env, l.Meta, verdict == plugin.Allow); err != nil {
		h.recordTrace(env.Trace, id, "guard_denied", err.Error())
		h.publish(id, "guard.denied", map[string]any{"from": env.From.String(), "to": env.To.String(), "reason": err.Error()})
		return err
	}
	// 回信核对（11 回信核对）：存在、未过期、未用过、发送者 = 预期回复方，只收第一条。
	if env.Kind == mail.KindReply {
		if _, err := h.DB.ClaimCorr(ctx, env.Corr, id, env.From.String()); err != nil {
			h.recordTrace(env.Trace, id, "guard_denied", "corr")
			h.publish(id, "guard.denied", map[string]any{"from": env.From.String(), "to": env.To.String(),
				"corr": env.Corr, "reason": "corr"})
			return fmt.Errorf("%w: corr %s", err, env.Corr)
		}
	}
	// 内核守卫（T3 分片二接入联系名单 / caps / hop）。
	if h.Guard != nil {
		if err := h.Guard(&env); err != nil {
			h.recordTrace(env.Trace, id, "guard_denied", err.Error())
			h.publish(id, "guard.denied", map[string]any{"from": env.From.String(), "to": env.To.String(),
				"reason": err.Error()})
			return err
		}
	}
	if env.Kind == mail.KindControl || env.Mode == mail.ModeControl {
		return h.control(ctx, id, env)
	}
	kind := store.InboxInput
	switch env.Mode {
	case mail.ModeContext:
		kind = store.InboxContext
	case mail.ModeNotice:
		kind = store.InboxNotice
	}
	body, _ := json.Marshal(session.Content{session.NewTextPart(env.Body)})
	eb, _ := json.Marshal(env)
	trust := "trusted"
	if !env.Origin.Trusted() {
		trust = "untrusted"
	}
	fresh, err := h.DB.Admit(ctx, id, store.InboxItem{ItemID: env.ID, Kind: kind,
		Input: string(body), Trust: trust, Envelope: string(eb)})
	if err != nil {
		return err
	}
	if !fresh {
		return nil // 幂等：同 id 只投一次
	}
	h.publish(id, "mail.delivered", map[string]any{"from": env.From.String(), "kind": string(env.Kind),
		"mode": string(env.Mode), "trace": env.Trace})
	h.recordTrace(env.Trace, id, "envelope", fmt.Sprintf("%s -> %s (%s/%s)", env.From, env.To, env.Kind, env.Mode))
	// 未打开的会话（休眠或从没在本节点开过）：Open 抢租约、恢复，drainInbox 会取信。
	if _, open := h.Get(id); !open {
		if _, err := h.Open(ctx, id); err != nil {
			return err
		}
		return nil
	}
	if a, ok := h.getActor(id); ok {
		select {
		case a.in <- Command{Kind: Deliver}:
		default: // 队列满：actor 处理完手上命令后会自己 drainInbox
		}
	}
	return nil
}

// control 处理 control 信封（中断 / 关闭）：不进模型。
func (h *Host) control(ctx context.Context, id string, env mail.Envelope) error {
	cmd := strings.TrimSpace(env.Body)
	l, err := h.DB.Load(ctx, id, nil)
	if err != nil {
		return err
	}
	if env.Origin == mail.OriginExternal { // 外部来源不能发 control（08 联系名单：外部一律降权，不能控制会话）
		h.recordTrace(env.Trace, id, "guard_denied", "control_external")
		h.publish(id, "guard.denied", map[string]any{"from": env.From.String(), "reason": "control_external"})
		return fmt.Errorf("host: 外部来源 %s 不能发送 control 信封", env.From)
	}
	if !controlAllowed(env.From, l.Meta) {
		h.recordTrace(env.Trace, id, "guard_denied", "control")
		h.publish(id, "guard.denied", map[string]any{"from": env.From.String(), "reason": "control"})
		return fmt.Errorf("host: %s 无权发送 control 信封", env.From)
	}
	hd, open := h.Get(id)
	if !open {
		if _, err := h.Open(ctx, id); err != nil {
			return err
		}
		hd, open = h.Get(id)
		if !open {
			return ErrClosed
		}
	}
	switch cmd {
	case "interrupt", "cancel":
		if err := hd.Do(Command{Kind: Cancel}); err != nil {
			return err
		}
	case "close":
		if err := hd.Do(Command{Kind: Close}); err != nil {
			return err
		}
	default:
		return fmt.Errorf("host: 未知 control 指令 %q（支持 interrupt / close）", cmd)
	}
	h.publish(id, "mail.delivered", map[string]any{"from": env.From.String(), "kind": "control", "mode": "control"})
	return nil
}

// controlAllowed control 信封只允许会话创建者、父会话、内核与登记组件（08 8.2 / P7）。
func controlAllowed(from mail.Address, meta store.SessionMeta) bool {
	switch from.Kind {
	case mail.AddrHuman:
		return meta.Creator != "" && from.ID == meta.Creator
	case mail.AddrSession:
		return meta.ParentID != "" && from.ID == meta.ParentID
	case mail.AddrComponent:
		return true // 组件经 Mailer 登记名单校验（T3 分片二收紧）
	default:
		return false
	}
}

// getActor 取已注册的 actor（不加锁语义：调用方只做幂等投递）。
func (h *Host) getActor(id string) (*actor, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	a, ok := h.actors[id]
	return a, ok
}

// parseEnvelope 解析 inbox 项里的信封；空/损坏返回 false（按旧格式处理）。
func parseEnvelope(it store.InboxItem) (mail.Envelope, bool) {
	if it.Envelope == "" {
		return mail.Envelope{}, false
	}
	var e mail.Envelope
	if err := json.Unmarshal([]byte(it.Envelope), &e); err != nil {
		return mail.Envelope{}, false
	}
	return e, true
}

// itemSubject 信封的权限主体（from + budget_ref）；非信封项返回空。
func itemSubject(it store.InboxItem) string {
	if e, ok := parseEnvelope(it); ok {
		return e.Subject()
	}
	return ""
}

// collectInboxBatch 从 inbox 里取一批要合并成一条消息的项（13 13.3 合并规则）：
// 找第一个待处理的 input，把它之前的同权限主体 context 与旧格式 note 一起带上。
// 没有 input（只有背景/通知）返回 nil，等下一次 wake。
func collectInboxBatch(items []store.InboxItem, skip map[string]bool) []store.InboxItem {
	idx := -1
	for i := range items {
		if skip[items[i].ItemID] {
			continue
		}
		if items[i].Kind == store.InboxInput {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}
	subj := itemSubject(items[idx])
	var out []store.InboxItem
	for i := 0; i <= idx; i++ {
		it := items[i]
		if skip[it.ItemID] {
			continue
		}
		switch it.Kind {
		case store.InboxInput, store.InboxNote:
			out = append(out, it)
		case store.InboxContext:
			if subj != "" && itemSubject(it) == subj {
				out = append(out, it)
			}
		}
	}
	// 13 13.3：同一权限主体的 wake 合并成一条用户消息；不同主体分轮。
	// 第一个 input 之后，继续并入同主体的 input / context，遇到不同主体的 input 即停
	// （不同主体按到达顺序分轮，不能越序）。旧格式（无信封）不合并，保持逐条处理。
	for i := idx + 1; subj != "" && i < len(items); i++ {
		it := items[i]
		if skip[it.ItemID] {
			continue
		}
		s := itemSubject(it)
		if s == subj && (it.Kind == store.InboxInput || it.Kind == store.InboxContext) {
			out = append(out, it)
			continue
		}
		if s != subj && it.Kind == store.InboxInput {
			break
		}
	}
	return out
}

// renderBatch 把一批 inbox 项变成一条用户消息内容（按 13 的 <msg> 格式），
// 并返回这批里第一封信封（用于 meta 的 from / origin）与是否不可信。
func renderBatch(items []store.InboxItem) (session.Content, mail.Envelope, bool) {
	var content session.Content
	var first mail.Envelope
	hasEnv, untrusted := false, false
	for _, it := range items {
		if e, ok := parseEnvelope(it); ok {
			content = append(content, session.NewTextPart(mail.Render([]mail.Envelope{e})))
			if !hasEnv {
				first, hasEnv = e, true
			}
			if !e.Origin.Trusted() {
				untrusted = true
			}
			continue
		}
		var c session.Content
		if json.Unmarshal([]byte(it.Input), &c) != nil {
			continue
		}
		if it.Kind == store.InboxNote {
			c = append(session.Content{session.NewTextPart("[补报] ")}, c...)
		}
		content = append(content, c...)
		if it.Trust == "untrusted" {
			untrusted = true
		}
	}
	return content, first, untrusted
}

// envelopeMeta 按信封填条目 meta（source / from / trust）。
func envelopeMeta(e mail.Envelope, untrusted bool) map[string]any {
	m := map[string]any{session.MetaSource: "user", session.MetaTrust: "trusted"}
	if e.From.Kind != "" {
		m[session.MetaFrom] = e.From.ID
		switch e.From.Kind {
		case mail.AddrAgent:
			m[session.MetaSource] = "agent"
		case mail.AddrComponent:
			m[session.MetaSource] = "component"
		}
	}
	if untrusted {
		m[session.MetaTrust] = "untrusted"
	}
	return m
}

// applyNotices 把 notice 信封转成一条 status 条目写进历史（12：notice 进状态更新），
// 并在同一个检查点里确认消费。没有 notice 返回 false。
func (a *actor) applyNotices(ctx context.Context, items []store.InboxItem) (bool, error) {
	var parts []string
	var consume []string
	untrusted := false
	for _, it := range items {
		if it.Kind != store.InboxNotice {
			continue
		}
		e, ok := parseEnvelope(it)
		if !ok {
			continue
		}
		parts = append(parts, fmt.Sprintf(`<item source=%q key=%q>%s</item>`,
			mail.EscapeAttr(e.From.ID), mail.EscapeAttr(e.ID), mail.EscapeText(e.Body)))
		consume = append(consume, it.ItemID)
		if !e.Origin.Trusted() {
			untrusted = true
		}
	}
	if len(consume) == 0 {
		return false, nil
	}
	text := "<status-update>" + strings.Join(parts, "") + "</status-update>"
	m := map[string]any{session.MetaSource: "notice", session.MetaTrust: "trusted", "notice": true}
	if untrusted {
		m[session.MetaTrust] = "untrusted"
	}
	a.processing = inboxSet(consume)
	if _, err := a.ag.Core.Inject(session.KindStatus, text, m); err != nil {
		a.processing = nil
		return false, err
	}
	if err := a.ag.Commit(ctx, consume...); err != nil {
		a.processing = nil
		return false, err
	}
	a.processing = nil
	return true, nil
}

// batchCaps 一批信封的 caps 交集（信封 caps 已是触发链上的交集；不同发送方再取交集）。
// nil = 不限；空集不参与（表示该封没有收紧）。返回拷贝，避免调用方改到信封数据。
func batchCaps(items []store.InboxItem) []string {
	var out []string
	for _, it := range items {
		e, ok := parseEnvelope(it)
		if !ok || len(e.Caps) == 0 {
			continue
		}
		out = intersectCaps(out, e.Caps)
	}
	return out
}

// intersectCaps 两个授权范围的交集；nil 表示不限，空集按空集处理。
func intersectCaps(a, b []string) []string {
	if a == nil {
		return append([]string(nil), b...)
	}
	if b == nil {
		return append([]string(nil), a...)
	}
	keep := make(map[string]bool, len(b))
	for _, s := range b {
		keep[s] = true
	}
	var out []string
	for _, s := range a {
		if keep[s] {
			out = append(out, s)
		}
	}
	return out
}

// guardContact 实现 08 D4 的联系名单第一版：创建者本人、登记组件（经 Mailer，
// from=component:<名>）以及带 corr 的回信可以投递；agent/session 直投留待联系
// 名单配置（[use.agent] contact）再接。
//
// allow 为 true 表示组件登记的 Allow 规则放开了「往 session: 投递」这条缺省
// 限制（D4）；联系名单以外的硬约束（跳数、租户、预算）不受它影响。
func guardContact(env mail.Envelope, meta store.SessionMeta, allow bool) error {
	switch env.From.Kind {
	case mail.AddrHuman:
		if meta.Creator != "" && env.From.ID != meta.Creator {
			if allow {
				return nil
			}
			return fmt.Errorf("%w: %s 不是创建者 %s", ErrNotAllowed, env.From.ID, meta.Creator)
		}
	case mail.AddrComponent:
		return nil
	default:
		if env.Corr == "" && !allow {
			return fmt.Errorf("%w: %s 直投需要 corr", ErrNotAllowed, env.From.Kind)
		}
	}
	return nil
}

// FlushOutbox 派发待外发信封（08 9.6）：检查点提交后调用。派发成功才标记已发；
// 被永久拒绝的（会话不存在/已关闭/不在联系名单/跳数超限/corr 不符）也标记，
// 避免毒消息反复重试。临时错误留在表里等下次重试。
func (h *Host) FlushOutbox(ctx context.Context) (int, error) {
	items, err := h.DB.Outbox(ctx, 64)
	if err != nil {
		return 0, err
	}
	sent := 0
	for _, it := range items {
		var env mail.Envelope
		if err := json.Unmarshal(it.Envelope, &env); err != nil {
			_ = h.DB.MarkOutbox(ctx, it.ID)
			continue
		}
		err := h.Deliver(ctx, env)
		permanent := errors.Is(err, ErrSessionNotFound) || errors.Is(err, ErrClosed) ||
			errors.Is(err, ErrNotAllowed) || errors.Is(err, ErrTooManyHops) || errors.Is(err, store.ErrCorrRejected)
		if err == nil {
			sent++
			permanent = true
		}
		if permanent {
			if merr := h.DB.MarkOutbox(ctx, it.ID); merr != nil {
				return sent, merr
			}
		}
	}
	return sent, nil
}
