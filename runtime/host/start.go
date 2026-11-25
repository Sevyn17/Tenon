package host

// 派出会话（Mortise docs/76 C2）：session_start 的异步两种用法都经这里——
//   - 独立会话（relation=independent）：顶层会话，ParentID 空，CreatorSession = 派出方；
//   - 异步子会话（relation=child、reply=notify|none）：子会话，ParentID = CreatorSession = 派出方，不等它做完。
//
// 两者都写创建链（深度 = 派出方 + 1，写死两层）与回投方式 ReplyMode，然后以 From = session:<派出方> 投第一封信
// （Kind=request、Mode=wake、带新 corr）。信的 caps、hop、trace、origin、budget_ref 由调用方按派出方本回合写好
// （agent.Stamp），所以新会话这一轮的工具不宽于派出方，整条链记在同一预算账上。
// 回投在被派会话那边：只有由这封带 corr 的信触发的回合结束时才回投（actor.replyToCreator）。

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/Sevyn17/tenon/runtime/agent"
	"github.com/Sevyn17/tenon/runtime/mail"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
)

// 回投方式（SessionMeta.ReplyMode）。
const (
	ReplyWait       = "wait"        // 同步等结果（子会话；不回投，结果由工具直接返回）
	ReplyNotify     = "notify"      // 回投 notice：不开回合，进创建者的状态更新，按不可信显示
	ReplyNotifyWake = "notify_wake" // 回投并唤醒创建者开一轮（信封 caps 交集照常生效）
	ReplyNone       = "none"        // 不回投
)

// ReplyMaxBytes 回投正文上限（docs/76 O3）：被派会话最后一条回复截到这么长，后面附会话链接。
const ReplyMaxBytes = 4096

// StartOptions 派出一个会话。
type StartOptions struct {
	Creator    string // 派出方会话 id（必填）
	ID         string // 新会话 id；空 = 子会话 <派出方>/sub-<随机>，独立会话 ag-<随机>
	Definition string // 新会话的定义名
	Factory    Factory
	Child      bool   // true = 子会话（ParentID = 派出方）；false = 独立会话
	Origin     string // 会话行 origin；空 = 子会话 subagent、独立会话 agent
	Task       string
	ReplyMode  string // notify / notify_wake / none
	Spawn      SpawnOptions
	// Stamp 按派出方本回合写信封的内核字段（from、hop、trace、caps、origin、budget_ref）；
	// nil = 只写 from = session:<派出方>（测试与无回合上下文的调用）。
	Stamp func(*mail.Envelope)
}

// PendingCreator 正在新建的会话的派出方（StartSession 建会话前登记、建完即清）。
// 会话构造（assemble.Builder）据此在会话行落库之前就按派出方算创建链深度与权限上限。
func (h *Host) PendingCreator(id string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pendingCreator[id]
}

// StartSession 按 o 建会话并投第一封信，返回新会话 id，不等待。
func (h *Host) StartSession(ctx context.Context, o StartOptions) (string, error) {
	if o.Creator == "" {
		return "", errors.New("host: StartSession 缺派出方")
	}
	switch o.ReplyMode {
	case ReplyNotify, ReplyNotifyWake, ReplyNone:
	default:
		return "", fmt.Errorf("host: StartSession 的回投方式只能是 notify / notify_wake / none，得 %q", o.ReplyMode)
	}
	cl, err := h.head(ctx, o.Creator)
	if err != nil {
		return "", fmt.Errorf("host: 读派出方会话 %s：%w", o.Creator, err)
	}
	depth, root := cl.Meta.Depth()+1, cl.Meta.Root()
	if depth > store.MaxChainDepth {
		return "", fmt.Errorf("%w：会话 %s 已在创建链第 %d 层，不能再建会话（最多 %d 层）", ErrChainDepth, o.Creator, depth-1, store.MaxChainDepth)
	}
	id := o.ID
	if id == "" {
		if o.Child {
			id = o.Creator + "/sub-" + NewItemID()[24:]
		} else {
			id = "ag-" + NewItemID()
		}
	}
	if ok, err := h.DB.Exists(ctx, id); err != nil {
		return "", err
	} else if ok {
		return "", fmt.Errorf("%w: 会话 %s 已存在", store.ErrConflict, id)
	}
	origin := o.Origin
	if origin == "" {
		origin = "agent"
		if o.Child {
			origin = "subagent"
		}
	}
	f := o.Factory
	if f == nil {
		if o.Child {
			f = h.ChildFactory()
		} else {
			f = h.New
		}
	}
	h.mu.Lock()
	if h.pendingCreator == nil {
		h.pendingCreator = map[string]string{}
	}
	h.pendingCreator[id] = o.Creator
	if o.Child && !o.Spawn.IsZero() {
		if h.spawnOpts == nil {
			h.spawnOpts = map[string]SpawnOptions{}
		}
		h.spawnOpts[id] = o.Spawn
	}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.pendingCreator, id)
		delete(h.spawnOpts, id)
		h.mu.Unlock()
	}()
	meta := store.SessionMeta{Origin: origin, CreatorSession: o.Creator, ChainDepth: depth, ChainRoot: root,
		ReplyMode: o.ReplyMode, Workspace: cl.Meta.Workspace}
	if o.Child {
		meta.ParentID = o.Creator
		if !o.Spawn.IsZero() {
			raw, err := store.MergeMeta(nil, map[string]any{MetaSpawn: o.Spawn})
			if err != nil {
				return "", err
			}
			meta.Meta = raw
		}
	}
	hd, err := h.open(ctx, id, o.Definition, f, func(ag *agent.Agent, cfg *session.Config) error {
		if ag.Def != nil {
			meta.AgentID, meta.AgentHash = ag.Def.ID, ag.Def.Version
		}
		_, err := ag.OpenAs(ctx, cfg, meta)
		return err
	})
	if err != nil {
		return "", err
	}
	kind := "new"
	if o.Child {
		kind = origin
	}
	if err := h.created(ctx, id, Origin{Kind: kind, ParentID: meta.ParentID}); err != nil {
		_ = hd.Do(Command{Kind: Close})
		return "", err
	}
	env := mail.Envelope{To: mail.Address{Kind: mail.AddrSession, ID: id}, Kind: mail.KindRequest, Mode: mail.ModeWake,
		Corr: mail.NewID(), Body: o.Task}
	if o.Stamp != nil {
		o.Stamp(&env)
	} else {
		env.From = mail.Address{Kind: mail.AddrSession, ID: o.Creator}
	}
	if err := h.deliver(ctx, env); err != nil {
		return id, fmt.Errorf("host: 会话 %s 已建，投第一封信失败：%w", id, err)
	}
	return id, nil
}

// chainContact 创建链上的直投（docs/76 §4.2）：发件会话是收件会话的创建者，或收件会话是发件会话的创建者。
// 只沿创建链：链外的会话互投仍按联系名单（guardContact）。
func (h *Host) chainContact(ctx context.Context, env mail.Envelope, target store.SessionMeta, id string) bool {
	if env.From.Kind != mail.AddrSession || env.From.ID == "" {
		return false
	}
	if target.CreatorSession == env.From.ID {
		return true
	}
	sl, err := h.head(ctx, env.From.ID)
	return err == nil && sl.Meta.CreatorSession == id
}

// replyToCreator 被派会话的回投（docs/76 §4.3）：只有这一轮由创建者那封带 corr 的信触发、且会话行的回投方式是
// notify / notify_wake 时才回投。回投信随本回合结束的检查点进发件箱（08 9.6），信封 id 由 corr 定，重复回投按 id 幂等。
func (a *actor) replyToCreator(ctx context.Context, end session.EndReason) {
	corr, from := a.turnCorr, a.turnFrom
	a.turnCorr, a.turnFrom = "", ""
	if corr == "" || from == "" {
		return
	}
	l, err := a.h.head(ctx, a.id)
	if err != nil || l.Meta.CreatorSession != from {
		return
	}
	var mode mail.Mode
	switch l.Meta.ReplyMode {
	case ReplyNotify:
		mode = mail.ModeNotice
	case ReplyNotifyWake:
		mode = mail.ModeWake
	default:
		return
	}
	text, _ := lastAssistant(a.ag.CommittedHistory())
	body := fmt.Sprintf("[会话 session:%s（%s）这一轮结束：%s]\n%s", a.id, l.Meta.AgentID, end.String(), truncateBytes(text, ReplyMaxBytes))
	env := mail.Envelope{ID: "rp-" + corr, To: mail.Address{Kind: mail.AddrSession, ID: from},
		Kind: mail.KindNotify, Mode: mode, Corr: corr, Body: body}
	if err := a.ag.Outbound(env); err != nil {
		a.h.publish(a.id, "mail.failed", map[string]any{"error": "回投：" + err.Error()})
	}
}

// truncateBytes 按字节截断（不切断 UTF-8 字符），截断时末尾加"…"。
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s + "…"
}

// ScanMail 启动扫描（docs/76 §4.5）：打开信箱里有可唤醒信件（input / notice / queued）、未删除的会话，不论状态。
// 上次进程退出时停在 active 的会话、子会话的未消费信件，靠它在重启后得到处理（子会话经 isChild 走 Child 工厂）。
func (h *Host) ScanMail(ctx context.Context) (int, error) {
	kinds := []string{store.InboxInput, store.InboxNotice, store.InboxQueued}
	var ids []string
	if sc, ok := h.DB.(store.MailScanner); ok {
		var err error
		if ids, err = sc.SessionsWithMail(ctx, kinds, ScanBatch); err != nil {
			return 0, err
		}
	} else {
		for _, st := range []string{store.StatusActive, store.StatusSleeping} {
			got, err := h.DB.SessionsWithStatus(ctx, st, ScanBatch)
			if err != nil {
				return 0, err
			}
			for _, id := range got {
				if items, err := h.DB.Inbox(ctx, id); err == nil && wakeable(items) {
					ids = append(ids, id)
				}
			}
		}
	}
	n := 0
	for _, id := range ids {
		if _, err := h.Open(ctx, id); err == nil {
			n++
		}
	}
	return n, nil
}

// OutboxRetryEvery 发件箱周期重试间隔（docs/76 §4.5）：临时失败交还的外发不必等下一次检查点或重启。
const OutboxRetryEvery = 30 * time.Second

// OutboxLoop 每 every 派发一次发件箱，直到 ctx 结束；宿主关闭后不再派发。
func (h *Host) OutboxLoop(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = OutboxRetryEvery
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.flushMu.Lock()
			closed := h.closed
			h.flushMu.Unlock()
			if closed {
				return
			}
			if _, err := h.FlushOutbox(ctx); err != nil && ctx.Err() == nil {
				h.publish("", "mail.failed", map[string]any{"error": err.Error()})
			}
		}
	}
}
