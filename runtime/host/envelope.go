package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Sevyn17/tenon/runtime/agent"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/mail"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
)

// agentSessionID 不带会话键的 agent: 投递所建会话的 id：由（定义, 创建者, 信封 id）确定，重投得到同一个 id（P1-17）。
// AgentSessionID 投给 agent:<def> 的第一封信（不带会话键）落进的会话 id：由（定义, 创建者, 信封 id）确定。
// SDK 用它在投递之前就订阅这个会话的事件（流式输出一个字都不漏）。
func AgentSessionID(def, creator, envID string) string { return agentSessionID(def, creator, envID) }

func agentSessionID(def, creator, envID string) string {
	sum := sha256.Sum256([]byte(def + "\x00" + creator + "\x00" + envID))
	return "ag-" + hex.EncodeToString(sum[:16])
}

// ErrSessionNotFound 会话地址只投不建（13 13.3：查不到就拒绝，不自动新建）。
var ErrSessionNotFound = errors.New("host: 会话不存在")

// ErrTooManyHops 信封跳数超过上限（08 D4：跳数超限截断回错）。
var ErrTooManyHops = errors.New("host: 信封跳数超过上限")

// ErrNotAllowed 发送方不在目标会话的联系名单里（08 D4）。
var ErrNotAllowed = errors.New("host: 发送方不在联系名单里")

// MaxHop 单封信封允许的最大跳数。
const MaxHop = 8

// Deliver 投递一封来自入口（人 / 应用 / 组件）的信封（13 13.3）：session: 只查不建；
// agent: 带 key 时查或建（B19）；human: 发布给界面。
//
// 内核字段一律由内核写（09 9.6 D2，调用方传入的值忽略）：hop 从 0 起算，trace 新分配，
// caps 清空（入口不收窄、也不能放宽），origin 只能降不能升（组件发来的最高是 agent）。
// 会话之间的转发（发件箱）保留链上已有的内核字段，见 deliverQueued。
//
// 返回前信封已落 inbox（幂等：同 id 只算一次）；回合中到达的 wake 会在下一次调模型前合并插入。
func (h *Host) Deliver(ctx context.Context, env mail.Envelope) error {
	env.Hop, env.Trace, env.Caps = 0, "", nil
	if env.From.Kind == mail.AddrComponent && env.Origin != mail.OriginExternal {
		env.Origin = mail.OriginAgent // 组件不能自称 human
	}
	return h.deliver(ctx, env)
}

// deliverQueued 派发会话发件箱里的信封：内核字段是发送会话的回合在提交时写好的
// （见 StampOutbound），沿链保留，只在这里继续加跳数。
func (h *Host) deliverQueued(ctx context.Context, env mail.Envelope) error {
	if env.Hop < 0 {
		env.Hop = 0
	}
	return h.deliver(ctx, env)
}

func (h *Host) deliver(ctx context.Context, env mail.Envelope) error {
	if err := env.Normalize(); err != nil {
		return err
	}
	if env.Trace == "" { // 一次任务共享 trace id（08 8.7）：入口处由内核分配。
		env.Trace = mail.NewID()
	}
	// 跳数（08 D4）：每投递一次加一，超过上限截断并记 guard.denied。
	env.Hop++
	if env.Hop > MaxHop {
		h.guardDenied(env, env.To.ID, "hop")
		return fmt.Errorf("%w: %d > %d", ErrTooManyHops, env.Hop, MaxHop)
	}
	switch env.To.Kind {
	case mail.AddrSession:
		return h.deliverSession(ctx, env)
	case mail.AddrAgent:
		return h.deliverAgent(ctx, env)
	case mail.AddrHuman:
		return h.deliverHuman(ctx, env)
	default:
		return fmt.Errorf("%w：%s", ErrUnsupportedAddr, env.To.String())
	}
}

// DeliverAgent 入口投递给 agent:<定义名>，返回信封落进的会话 id；bizMeta 非空时写进新建会话的业务 meta。
// 与 Deliver 同一套入口规则与守卫（会话打开守卫、投递守卫、联系名单）。SDK 的 Ask 走这里
// （2026-11-04 审查 P1-24：原先 Ask 自己建会话、直接 Send，绕开了守卫，每次还构造两次会话）。
func (h *Host) DeliverAgent(ctx context.Context, env mail.Envelope, bizMeta json.RawMessage) (string, error) {
	env.Hop, env.Trace, env.Caps = 0, "", nil
	if env.From.Kind == mail.AddrComponent && env.Origin != mail.OriginExternal {
		env.Origin = mail.OriginAgent
	}
	if env.To.Kind != mail.AddrAgent {
		return "", fmt.Errorf("%w：DeliverAgent 只投 agent: 地址，得 %s", mail.ErrInvalid, env.To)
	}
	if err := env.Normalize(); err != nil {
		return "", err
	}
	env.Trace = mail.NewID()
	env.Hop++
	return h.deliverAgentID(ctx, env, bizMeta)
}

// deliverAgent 投给 agent:<定义名>：带 key 时按（定义, 创建者, key）查或建会话（B19）。
func (h *Host) deliverAgent(ctx context.Context, env mail.Envelope) error {
	_, err := h.deliverAgentID(ctx, env, nil)
	return err
}

// deliverAgentID 同 deliverAgent，返回落进的会话 id；bizMeta 只在新建会话时写入。
func (h *Host) deliverAgentID(ctx context.Context, env mail.Envelope, bizMeta json.RawMessage) (string, error) {
	creator := ""
	if env.From.Kind == mail.AddrHuman {
		creator = env.From.ID
	}
	if env.Key != "" {
		id, err := h.DB.FindSessionByKey(ctx, env.To.ID, creator, env.Key)
		if err == nil {
			env.To = mail.Address{Kind: mail.AddrSession, ID: id}
			return id, h.deliverSession(ctx, env)
		}
		if !errors.Is(err, store.ErrNotFound) {
			return "", err
		}
	}
	// 新建会话前守卫（08 8.1）：可联系名单、所属树深度等由组件规则表达。
	if v, why := h.evalGuard(component.GuardRequest{Point: component.GuardSessionOpen, Envelope: &env, Agent: env.To.ID}); v == component.Deny {
		h.guardDenied(env, "", why, "agent", env.To.ID)
		return "", fmt.Errorf("%w: %s", ErrNotAllowed, why)
	}
	// 联系名单与投递守卫在建会话之前就过一遍（C7）：新会话的创建者就是本信的发送者，
	// 按它算一遍 deliverSession 会做的检查——被拒的信不该留下一个已经打开的空会话。
	id := "ag-" + NewItemID()
	if env.Key == "" {
		// 不带会话键：会话 id 由（定义, 创建者, 信封 id）确定。发件箱是至少一次投递，重试拿到同一个 id，
		// 已建好的会话直接投（inbox 按信封 id 幂等），不再重复建会话（2026-11-04 审查 P1-17）。
		id = agentSessionID(env.To.ID, creator, env.ID)
		noEntries := math.MaxInt // 只读会话行，不读条目
		if l, err := h.DB.Load(ctx, id, &noEntries); err == nil {
			if l.Meta.AgentID != env.To.ID {
				return "", fmt.Errorf("host: 会话 %s 已存在且不是 %s 的会话", id, env.To.ID)
			}
			env.To = mail.Address{Kind: mail.AddrSession, ID: id}
			return id, h.deliverSession(ctx, env)
		} else if !errors.Is(err, store.ErrNotFound) {
			return "", err
		}
	}
	meta := store.SessionMeta{ID: id, AgentID: env.To.ID, Creator: creator, SessionKey: env.Key, Origin: "agent", Meta: bizMeta, Workspace: h.Workspace}
	probe := env
	probe.To = mail.Address{Kind: mail.AddrSession, ID: id}
	verdict, reason := h.evalGuard(component.GuardRequest{Point: component.GuardDeliver, Envelope: &probe})
	if verdict == component.Deny {
		h.guardDenied(env, "", reason)
		return "", fmt.Errorf("%w: %s", ErrNotAllowed, reason)
	}
	if err := guardContact(probe, meta, verdict == component.Allow); err != nil {
		h.guardDenied(env, "", err.Error())
		return "", err
	}
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
				return sid, h.deliverSession(ctx, env)
			}
		}
		// 同一封信并发重投：另一路已建好确定 id 的会话，转投过去。
		if env.Key == "" && errors.Is(err, store.ErrConflict) {
			env.To = mail.Address{Kind: mail.AddrSession, ID: id}
			return id, h.deliverSession(ctx, env)
		}
		return "", err
	}
	env.To = mail.Address{Kind: mail.AddrSession, ID: id}
	return id, h.deliverSession(ctx, env)
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
	// 只读会话行一次（审查 P1-34：原先 Exists + SessionStatus + 整份日志 Load 共 3 次往返，投递成本随日志长度增长；
	// 已删除的会话 Load 本来就查不到，原先的 "deleted" 分支走不到）。
	l, err := h.head(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("%w: %s", ErrSessionNotFound, id)
	}
	if err != nil {
		return err
	}
	// 联系名单（08 D4）：默认只放开创建者本人、登记组件、以及带 corr 的回信；
	// 组件登记的 Allow 规则只能放开这条「往 session: 投递」的缺省限制（D4）。
	verdict, reason := h.evalGuard(component.GuardRequest{Point: component.GuardDeliver, Envelope: &env})
	if verdict == component.Deny {
		h.guardDenied(env, id, reason)
		return fmt.Errorf("%w: %s", ErrNotAllowed, reason)
	}
	if err := guardContact(env, l.Meta, verdict == component.Allow); err != nil {
		h.guardDenied(env, id, err.Error())
		return err
	}
	// 回信核对（11 回信核对）：存在、未过期、未用过、发送者 = 预期回复方，只收第一条。
	if env.Kind == mail.KindReply {
		if _, err := h.DB.ClaimCorr(ctx, env.Corr, id, env.From.String()); err != nil {
			h.guardDenied(env, id, "corr", "corr", env.Corr)
			return fmt.Errorf("%w: corr %s", err, env.Corr)
		}
		// 人工确认的回答：交给挂起的工具确认，不当作普通消息入信箱（11 11.2）。
		if h.resolveHuman(env.Corr, env.Body) {
			h.publish(id, "human.answered", map[string]any{"corr": env.Corr, "from": env.From.String(),
				"approve": decisionFromBody(env.Body)})
			h.recordTrace(env.Trace, id, "envelope", "human -> session:"+id+"（回信）")
			return nil
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
	// context 信件不开回合（13 13.3），不为它唤醒会话：留在信箱，等下一封 wake 一起进模型（审查 P1-16）。
	if _, open := h.Get(id); !open {
		if !wakeKind(kind) {
			return nil
		}
		if _, err := h.Open(ctx, id); err != nil {
			return err
		}
		return nil
	}
	if a, ok := h.getActor(id); ok {
		a.mailDirty.Store(true) // 回合中的步边界据此去读信箱（P1-36）
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
	l, err := h.head(ctx, id)
	if err != nil {
		return err
	}
	if env.Origin == mail.OriginExternal { // 外部来源不能发 control（08 联系名单：外部一律降权，不能控制会话）
		h.guardDenied(env, id, "control_external")
		return fmt.Errorf("host: 外部来源 %s 不能发送 control 信封", env.From)
	}
	if !controlAllowed(env.From, l.Meta) {
		h.guardDenied(env, id, "control")
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
		// 入队即算投递：回合中的 Close 要等回合结束，不能让发件箱派发同步等它（P0-6）。
		if err := hd.Post(Command{Kind: Close, Text: "control"}); err != nil {
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

// wakeKind 这类信件是否需要会话醒着处理：input 开回合、notice 进状态更新；
// context（背景）与旧格式 note（补报）只跟随下一条 input，单独存在时不唤醒、不阻止休眠。
func wakeKind(kind string) bool { return kind == store.InboxInput || kind == store.InboxNotice }

// wakeable 信箱里是否有需要会话醒着处理的信件（休眠、扫描唤醒、投递唤醒共用，审查 P1-16：
// 原先只要信箱非空就不睡，只有背景信件的会话永远常驻内存；背景信件还会唤醒休眠会话）。
func wakeable(items []store.InboxItem) bool {
	for _, it := range items {
		if wakeKind(it.Kind) {
			return true
		}
	}
	return false
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
	if err := a.ag.Core.Inject(session.KindStatus, text, m); err != nil {
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

// batchHop 一批信封里最大的跳数（本回合发出的信封从它继续计数，08 8.7）。
func batchHop(items []store.InboxItem) int {
	hop := 0
	for _, it := range items {
		if e, ok := parseEnvelope(it); ok && e.Hop > hop {
			hop = e.Hop
		}
	}
	return hop
}

// batchCaps 一批信封的 caps 交集（信封 caps 已是触发链上的交集；不同发送方再取交集）。
// nil = 不限（该封没有收紧）；空集 = 一个都不许，参与求交。返回拷贝，避免调用方改到信封数据。
func batchCaps(items []store.InboxItem) []string {
	var out []string
	for _, it := range items {
		e, ok := parseEnvelope(it)
		if !ok || e.Caps == nil {
			continue
		}
		out = intersectCaps(out, e.Caps)
	}
	return out
}

// intersectCaps 两个授权范围的交集；nil 表示不限。交集为空时返回非 nil 的空集（一个都不许），
// 不能退化成 nil——那会把「两边没有共同工具」变成「不限」。
func intersectCaps(a, b []string) []string {
	if a == nil {
		if b == nil {
			return nil
		}
		return append([]string{}, b...)
	}
	if b == nil {
		return append([]string{}, a...)
	}
	keep := make(map[string]bool, len(b))
	for _, s := range b {
		keep[s] = true
	}
	out := []string{}
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
		// 只有回信能凭 corr 进门，而且之后必须通过 ClaimCorr 核对；request / notify 带个 corr
		// 不算授权（否则随便填一个 corr 就绕过了联系名单）。
		if env.Kind != mail.KindReply && !allow {
			return fmt.Errorf("%w: %s 直投需要回信 corr", ErrNotAllowed, env.From.Kind)
		}
	}
	return nil
}

// kickFlush 在后台派发发件箱；已有派发在跑时只记一笔，由它跑完后再派一轮
// （本次提交的外发行可能在它读表之后才落库）。
func (h *Host) kickFlush() {
	h.flushMu.Lock()
	if h.flushing {
		h.flushAgain = true
		h.flushMu.Unlock()
		return
	}
	h.flushing = true
	h.flushMu.Unlock()
	go func() {
		for {
			if _, err := h.FlushOutbox(context.Background()); err != nil {
				h.publish("", "mail.failed", map[string]any{"error": err.Error()})
			}
			h.flushMu.Lock()
			if !h.flushAgain {
				h.flushing = false
				h.flushMu.Unlock()
				return
			}
			h.flushAgain = false
			h.flushMu.Unlock()
		}
	}()
}

// FlushOutbox 派发待外发信封（08 9.6）：检查点提交后调用。派发成功才标记已发；
// 被永久拒绝的（会话不存在/已关闭/不在联系名单/跳数超限/corr 不符）也标记，
// 避免毒消息反复重试。临时错误留在表里等下次重试。
// 同一个 Host 内串行执行（并发调用排队），同一条外发不会被两个调用同时读到并重复投递。
// 不要在 actor 协程里调用：投递 control 会同步等目标 actor 受理命令。
func (h *Host) FlushOutbox(ctx context.Context) (int, error) {
	h.flushRun.Lock()
	defer h.flushRun.Unlock()
	// 原子认领（C6 / A9）：同进程多个派发者、多个进程共用一个库都不会投同一条；
	// 认领带期限，派发者崩溃后到期由别人接手。
	items, err := h.DB.ClaimOutbox(ctx, h.leaseHolder(), OutboxClaimLease, OutboxBatch)
	if err != nil {
		return 0, err
	}
	sent := 0
	for _, it := range items {
		var env mail.Envelope
		if err := json.Unmarshal(it.Envelope, &env); err != nil {
			_ = h.DB.DeadOutbox(ctx, it.ID, "信封无法解析："+err.Error())
			continue
		}
		err := h.deliverQueued(ctx, env)
		switch {
		case err == nil:
			sent++
			if merr := h.DB.MarkOutbox(ctx, it.ID); merr != nil {
				return sent, merr
			}
		case permanentDeliverErr(err):
			// 永久失败（会话不存在 / 已关闭 / 不在联系名单 / 跳数超限 / corr 不符 / 信封不合法 / 地址不支持）：
			// 记死信、不再重试，免得毒消息反复占着队头。
			_ = h.DB.DeadOutbox(ctx, it.ID, err.Error())
			h.publish(it.SessionID, "mail.dead", map[string]any{"id": it.ID, "error": err.Error()})
		case it.Attempts >= OutboxMaxAttempts:
			_ = h.DB.DeadOutbox(ctx, it.ID, fmt.Sprintf("重试 %d 次仍失败：%v", it.Attempts, err))
			h.publish(it.SessionID, "mail.dead", map[string]any{"id": it.ID, "error": err.Error()})
		default: // 临时失败（如队列满）：交还，下次派发再试
			_ = h.DB.ReleaseOutbox(ctx, it.ID, h.leaseHolder())
		}
	}
	return sent, nil
}

// 发件箱派发参数（约束清单「信封与守卫」）。
const (
	OutboxBatch       = 64               // 每次认领条数
	OutboxClaimLease  = 30 * time.Second // 认领期限：派发者崩溃后多久可被别人接手
	OutboxMaxAttempts = 10               // 临时失败的最多尝试次数，用尽记死信
)

// permanentDeliverErr 投递错误是否是永久的（重试也不会成功）。
func permanentDeliverErr(err error) bool {
	return errors.Is(err, ErrSessionNotFound) || errors.Is(err, ErrClosed) ||
		errors.Is(err, ErrNotAllowed) || errors.Is(err, ErrTooManyHops) || errors.Is(err, store.ErrCorrRejected) ||
		errors.Is(err, mail.ErrInvalid) || errors.Is(err, ErrUnsupportedAddr)
}

// ErrUnsupportedAddr 投递目标的地址类型还不支持（如 component:）。
var ErrUnsupportedAddr = errors.New("host: 不支持投递到该地址")

// guardDenied 记一次守卫拒绝：trace 事件 + guard.denied 事件（2026-11-04 审查 P2：原先这两行在本文件里抄了 9 遍）。
// extra 是成对的附加键值（如 "corr", env.Corr）。
func (h *Host) guardDenied(env mail.Envelope, session, reason string, extra ...string) {
	h.recordTrace(env.Trace, session, "guard_denied", reason)
	data := map[string]any{"from": env.From.String(), "to": env.To.String(), "reason": reason}
	for i := 0; i+1 < len(extra); i += 2 {
		data[extra[i]] = extra[i+1]
	}
	h.publish(session, "guard.denied", data)
}
