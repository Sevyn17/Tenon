package appapi

// 会话命令（§二 命令表 sessions.* / session.*）。

import (
	"context"
	"encoding/json"
	"fmt"

	"ybase-kernel/core/host"
	"ybase-kernel/core/reqlog"
	"ybase-kernel/core/session"
	"ybase-kernel/core/store"
)

// SessionArgs 只带会话 id 的参数。
type SessionArgs struct {
	Session string `json:"session"`
}

func need(id string) error {
	if id == "" {
		return Errorf(CodeBadRequest, "缺少 session")
	}
	return nil
}

// handle 已打开会话的句柄；没打开就打开（会话不存在则新建）。
func (a *App) handle(ctx context.Context, id string) (host.Handle, error) {
	if err := need(id); err != nil {
		return host.Handle{}, err
	}
	if hd, ok := a.o.Host.Get(id); ok {
		return hd, nil
	}
	if a.o.Host.New == nil {
		return host.Handle{}, Errorf(CodeInternal, "appapi: 未设置会话构造器")
	}
	hd, err := a.o.Host.Open(ctx, id)
	return hd, wrap(err)
}

// opened 已打开会话的句柄；没打开返回 not_found（确认、取消等只对运行中的会话有意义）。
func (a *App) opened(id string) (host.Handle, error) {
	if err := need(id); err != nil {
		return host.Handle{}, err
	}
	hd, ok := a.o.Host.Get(id)
	if !ok {
		return host.Handle{}, Errorf(CodeNotFound, "会话 %s 未打开", id)
	}
	return hd, nil
}

// ListArgs sessions.list 参数。Cursor 为上一页的 next（updated_at 毫秒），0 = 第一页。
type ListArgs struct {
	Cursor int64 `json:"cursor,omitempty"`
	Limit  int   `json:"limit,omitempty"`
	All    bool  `json:"all,omitempty"` // 含子智能体 / 工作组成员会话
}

// ListResult sessions.list 结果。Next = 0 表示没有下一页。
type ListResult struct {
	Items []store.SessionInfo `json:"items"`
	Next  int64               `json:"next"`
}

// SessionsList 按更新时间倒序分页列会话（缺省只列顶层会话）。
func (a *App) SessionsList(ctx context.Context, in ListArgs) (ListResult, error) {
	limit := in.Limit
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	items, err := a.o.DB.ListSessions(ctx, in.Cursor, limit, !in.All)
	if err != nil {
		return ListResult{}, wrap(err)
	}
	out := ListResult{Items: items}
	if out.Items == nil {
		out.Items = []store.SessionInfo{}
	}
	if len(items) == limit {
		out.Next = items[len(items)-1].UpdatedAt
	}
	return out, nil
}

// OpenResult session.open 结果。
type OpenResult struct {
	ID       string `json:"id"`
	RunState string `json:"run_state"` // Idle / WaitingModel / WaitingTools
	LogLen   int    `json:"log_len"`
	Base     int    `json:"base"` // 最近一次压缩点（之前的条目只在 session.entries 显式要时给）
	Mode     string `json:"mode,omitempty"`
}

// Open 打开会话（不存在则新建），返回当前状态。之后用 Follow(after=0) 或 session.entries 取日志。
func (a *App) Open(ctx context.Context, in SessionArgs) (OpenResult, error) {
	hd, err := a.handle(ctx, in.Session)
	if err != nil {
		return OpenResult{}, err
	}
	snap := hd.Agent().Core.Snapshot()
	out := OpenResult{ID: hd.ID(), RunState: snap.Header.Status.String(), LogLen: snap.Header.LogLen, Base: snap.Header.CompactionBase}
	if a.o.Modes != nil {
		out.Mode = a.o.Modes.Get(hd.ID()).Mode
	}
	return out, nil
}

// EntriesArgs session.entries 参数：[From, To) 区间；Tail > 0 时取最后 Tail 条（忽略 From / To）。
// From 缺省为最近压缩点；To 缺省为日志末尾。
type EntriesArgs struct {
	Session string `json:"session"`
	From    *int   `json:"from,omitempty"`
	To      *int   `json:"to,omitempty"`
	Tail    int    `json:"tail,omitempty"`
}

// EntryRow 一条日志：完整日志下标 + 条目本体。
type EntryRow struct {
	Idx   int            `json:"idx"`
	Entry *session.Entry `json:"entry"`
}

// EntriesResult session.entries 结果。
type EntriesResult struct {
	Entries []EntryRow `json:"entries"`
	Base    int        `json:"base"`
	LogLen  int        `json:"log_len"`
}

// Entries 读已提交日志（只读，回合中也可用）。
func (a *App) Entries(ctx context.Context, in EntriesArgs) (EntriesResult, error) {
	if err := need(in.Session); err != nil {
		return EntriesResult{}, err
	}
	l, err := a.o.DB.Load(ctx, in.Session, nil)
	if err != nil {
		return EntriesResult{}, wrap(err)
	}
	h := l.Snapshot.Header
	from, to := h.CompactionBase, h.LogLen
	if in.From != nil {
		from = *in.From
	}
	if in.To != nil {
		to = *in.To
	}
	if in.Tail > 0 {
		from, to = max(h.LogLen-in.Tail, 0), h.LogLen
	}
	if from < 0 || to < from {
		return EntriesResult{}, Errorf(CodeBadRequest, "区间 [%d, %d) 不合法", from, to)
	}
	es, err := a.o.DB.Entries(ctx, in.Session, from, to)
	if err != nil {
		return EntriesResult{}, wrap(err)
	}
	out := EntriesResult{Entries: make([]EntryRow, len(es)), Base: h.CompactionBase, LogLen: h.LogLen}
	for i, e := range es {
		out.Entries[i] = EntryRow{Idx: from + i, Entry: e}
	}
	return out, nil
}

// SendArgs session.send 参数。ItemID 为幂等键（重发同一 id 只算一次），缺省自动生成。
type SendArgs struct {
	Session string `json:"session"`
	Text    string `json:"text"`
	ItemID  string `json:"item_id,omitempty"`
}

// Send 投递用户输入（落 inbox 即返回；回合中投递的排到下一回合）。
func (a *App) Send(ctx context.Context, in SendArgs) (Empty, error) {
	hd, err := a.handle(ctx, in.Session)
	if err != nil {
		return Empty{}, err
	}
	return Empty{}, wrap(hd.Do(host.Command{Kind: host.Send, ItemID: in.ItemID, Text: in.Text}))
}

// TextArgs 会话 + 文本参数。
type TextArgs struct {
	Session string `json:"session"`
	Text    string `json:"text"`
}

// Steer 回合中插话（下一步生效）；会话空闲时返回 not_idle。
func (a *App) Steer(_ context.Context, in TextArgs) (Empty, error) {
	hd, err := a.opened(in.Session)
	if err != nil {
		return Empty{}, err
	}
	return Empty{}, wrap(hd.Do(host.Command{Kind: host.Steer, Text: in.Text}))
}

// Cancel 取消进行中的回合（空闲时无操作）。
func (a *App) Cancel(_ context.Context, in SessionArgs) (Empty, error) {
	hd, err := a.opened(in.Session)
	if err != nil {
		return Empty{}, err
	}
	return Empty{}, wrap(hd.Do(host.Command{Kind: host.Cancel}))
}

// 确认决定。
const (
	DecisionOnce    = "once"    // 本次允许
	DecisionSession = "session" // 本次会话内都允许（生成会话规则）
	DecisionDeny    = "deny"
)

// ConfirmArgs session.confirm 参数。Session 为 human_requested 事件里的会话（可能是子会话）。
type ConfirmArgs struct {
	Session  string `json:"session"`
	CallID   string `json:"call_id"`
	Decision string `json:"decision"`
	// Tool / Args 可空：缺省取 App 记下的 human_requested 事件内容（decision=session 生成规则用）。
	Tool string `json:"tool,omitempty"`
	Args string `json:"args,omitempty"`
}

// ConfirmResult session.confirm 结果。Rules 为 decision=session 时新生成的会话规则。
type ConfirmResult struct {
	Rules []string `json:"rules,omitempty"`
}

// Confirm 回答确认请求。decision=session 时规则立即在内存生效，持久化到顶层会话头异步进行
// （回合中 SetExt 要等步边界才受理，而这一步正等着本次确认，同步等会死锁）；失败发 notice。
func (a *App) Confirm(_ context.Context, in ConfirmArgs) (ConfirmResult, error) {
	hd, err := a.opened(in.Session)
	if err != nil {
		return ConfirmResult{}, err
	}
	if in.CallID == "" {
		return ConfirmResult{}, Errorf(CodeBadRequest, "缺少 call_id")
	}
	pend := a.takePending(in.CallID)
	if in.Tool == "" {
		in.Tool, in.Args = pend.tool, pend.args
	}
	var out ConfirmResult
	ok := false
	switch in.Decision {
	case DecisionOnce:
		ok = true
	case DecisionDeny:
	case DecisionSession:
		if a.o.Modes == nil {
			return ConfirmResult{}, Errorf(CodeUnsupported, "未装权限插件，不能生成会话规则")
		}
		ok = true
		added, all := a.o.Modes.AllowForSession(in.Session, in.Tool, json.RawMessage(in.Args))
		out.Rules = added
		top := hd
		if t := topOf(in.Session); t != in.Session {
			if g, found := a.o.Host.Get(t); found {
				top = g
			}
		}
		_, key := a.o.Modes.ExtKeys()
		go func() {
			if err := top.Do(host.Command{Kind: host.SetExt, Key: key, Value: all}); err != nil {
				a.Notice(in.Session, fmt.Sprintf("[会话规则未能保存：%v]", err))
			}
		}()
	default:
		return ConfirmResult{}, Errorf(CodeBadRequest, "decision 只能是 once / session / deny，得到 %q", in.Decision)
	}
	return out, wrap(hd.Do(host.Command{Kind: host.Confirm, CallID: in.CallID, Approve: ok}))
}

// AnswerArgs session.answer 参数。
type AnswerArgs struct {
	AskID string `json:"ask_id"`
	Text  string `json:"text"`
}

// Answer 回答 ask 事件（需 Options.UI）。
func (a *App) Answer(_ context.Context, in AnswerArgs) (Empty, error) {
	if a.o.UI == nil {
		return Empty{}, Errorf(CodeUnsupported, "应用未使用事件化界面，不能经接口回答提问")
	}
	return Empty{}, a.o.UI.Answer(in.AskID, in.Text)
}

// RewindArgs session.rewind 参数：退回最近 Turns 个回合（缺省 1）。
type RewindArgs struct {
	Session string `json:"session"`
	Turns   int    `json:"turns,omitempty"`
}

// RewindResult session.rewind 结果：日志截到 To。
type RewindResult struct {
	To int `json:"to"`
}

// TurnStarts 已提交日志中可退回的回合起点（绝对下标，压缩点之前的不含）与日志长度。
func (a *App) TurnStarts(ctx context.Context, id string) ([]int, int, error) {
	l, err := a.o.DB.Load(ctx, id, nil)
	if err != nil {
		return nil, 0, wrap(err)
	}
	s := l.Snapshot
	var starts []int
	for i, e := range s.Entries {
		if e.Item.Kind == session.KindUser && (i == 0 || s.Entries[i-1].Turn != e.Turn) {
			starts = append(starts, s.Base+i)
		}
	}
	return starts, s.Header.LogLen, nil
}

// Rewind 退回回合（需空闲；回合中投递会排到回合结束）。
func (a *App) Rewind(ctx context.Context, in RewindArgs) (RewindResult, error) {
	n := in.Turns
	if n == 0 {
		n = 1
	}
	if n < 1 {
		return RewindResult{}, Errorf(CodeBadRequest, "turns 须 ≥ 1")
	}
	hd, err := a.handle(ctx, in.Session)
	if err != nil {
		return RewindResult{}, err
	}
	starts, _, err := a.TurnStarts(ctx, in.Session)
	if err != nil {
		return RewindResult{}, err
	}
	if n > len(starts) {
		return RewindResult{}, Errorf(CodeBadRequest, "只有 %d 个回合可退回（压缩点之前的不能退回）", len(starts))
	}
	to := starts[len(starts)-n]
	if err := hd.Do(host.Command{Kind: host.Rewind, To: to}); err != nil {
		return RewindResult{}, wrap(err)
	}
	return RewindResult{To: to}, nil
}

// CompactResult session.compact 结果。
type CompactResult struct {
	Summary      string `json:"summary"`
	Upto         int    `json:"upto"`
	TokensBefore any    `json:"tokens_before,omitempty"`
	TokensAfter  any    `json:"tokens_after,omitempty"`
}

// Compact 手动压缩：保留与自动压缩相同的尾部，切点之前交给压缩器总结；保留量不够切时整段压缩到末尾。
func (a *App) Compact(ctx context.Context, in SessionArgs) (CompactResult, error) {
	if err := need(in.Session); err != nil {
		return CompactResult{}, err
	}
	if a.o.Compactor == nil {
		return CompactResult{}, Errorf(CodeUnsupported, "未装配 compact-basic 插件")
	}
	hd, err := a.handle(ctx, in.Session)
	if err != nil {
		return CompactResult{}, err
	}
	l, err := a.o.DB.Load(ctx, in.Session, nil)
	if err != nil {
		return CompactResult{}, wrap(err)
	}
	core, err := session.Restore(l.Snapshot)
	if err != nil {
		return CompactResult{}, wrap(err)
	}
	view, meta, err := a.o.Compactor.Plan(ctx, in.Session, core, true)
	if err != nil {
		return CompactResult{}, wrap(err)
	}
	if view == nil {
		if view, meta, err = a.o.Compactor.At(ctx, in.Session, core, l.Snapshot.Header.LogLen); err != nil {
			return CompactResult{}, wrap(err)
		}
		if view == nil {
			return CompactResult{}, Errorf(CodeBadRequest, "没有可压缩的内容")
		}
	}
	sum := ""
	for _, p := range view.Summary {
		sum += p.Text
	}
	if err := hd.Do(host.Command{Kind: host.Compact, Text: sum, Upto: view.Upto}); err != nil {
		return CompactResult{}, wrap(err)
	}
	return CompactResult{Summary: sum, Upto: view.Upto, TokensBefore: meta["tokens_before"], TokensAfter: meta["tokens_after"]}, nil
}

// ForkArgs session.fork 参数。Upto < 0 或缺省 = 整段日志；Origin 为 fork（缺省）/ side。
type ForkArgs struct {
	Session string `json:"session"`
	To      string `json:"to"`
	Upto    *int   `json:"upto,omitempty"`
	Origin  string `json:"origin,omitempty"`
}

// ForkResult session.fork 结果。新会话已打开（Host.Get 可取句柄）。
type ForkResult struct {
	ID string `json:"id"`
}

// Fork 从会话分出新会话（来源须空闲；目标已存在返回 conflict）。
func (a *App) Fork(ctx context.Context, in ForkArgs) (ForkResult, error) {
	if err := need(in.Session); err != nil {
		return ForkResult{}, err
	}
	if in.To == "" {
		return ForkResult{}, Errorf(CodeBadRequest, "缺少 to")
	}
	if ok, err := a.o.DB.Exists(ctx, in.To); err != nil {
		return ForkResult{}, wrap(err)
	} else if ok {
		return ForkResult{}, Errorf(CodeConflict, "host: 会话 %s 已存在", in.To)
	}
	upto := -1
	if in.Upto != nil {
		upto = *in.Upto
	}
	hd, err := a.o.Host.Fork(ctx, in.Session, in.To, upto, in.Origin)
	if err != nil {
		return ForkResult{}, wrap(err)
	}
	return ForkResult{ID: hd.ID()}, nil
}

// Stats session.stats 结果：用量、缓存命中率与最近一次请求的上下文组成。HitRate < 0 表示无数据。
type Stats struct {
	Attempts    int                 `json:"attempts"`
	Reported    int                 `json:"reported"`
	Missing     int                 `json:"missing"`
	Errors      int                 `json:"errors"`
	Input       uint64              `json:"input"`
	Output      uint64              `json:"output"`
	CacheRead   uint64              `json:"cache_read"`
	CacheWrite  uint64              `json:"cache_write"`
	HitRate     float64             `json:"hit_rate"`
	ToolCalls   int                 `json:"tool_calls"`
	Compacts    int                 `json:"compacts"`
	Composition *reqlog.Composition `json:"composition,omitempty"`
	LastRequest string              `json:"last_request,omitempty"`
}

// Stats 会话统计（只读投影，回合中也可用）。
func (a *App) Stats(ctx context.Context, in SessionArgs) (Stats, error) {
	if err := need(in.Session); err != nil {
		return Stats{}, err
	}
	st, err := a.o.DB.SessionStats(ctx, in.Session)
	if err != nil {
		return Stats{}, wrap(err)
	}
	out := Stats{Attempts: st.Attempts, Reported: st.Reported, Missing: st.Missing, Errors: st.Errors,
		Input: st.Input, Output: st.Output, CacheRead: st.CacheRead, CacheWrite: st.CacheWrite,
		HitRate: st.HitRate(), ToolCalls: st.ToolCalls, Compacts: st.Compacts, LastRequest: st.LastRequest}
	if len(st.Composition) > 0 {
		var comp reqlog.Composition
		if json.Unmarshal(st.Composition, &comp) == nil {
			out.Composition = &comp
		}
	}
	return out, nil
}

// MaxStepsArgs session.max_steps 参数：N 为空只查询；0 = 不限。
type MaxStepsArgs struct {
	Session string  `json:"session"`
	N       *uint32 `json:"n,omitempty"`
}

// MaxStepsResult 当前（或设置后的）单回合步数上限；0 = 不限。
type MaxStepsResult struct {
	N uint32 `json:"n"`
}

// MaxSteps 查看或设置单回合步数上限（存在会话配置里；回合中到下一步边界生效）。
func (a *App) MaxSteps(ctx context.Context, in MaxStepsArgs) (MaxStepsResult, error) {
	hd, err := a.handle(ctx, in.Session)
	if err != nil {
		return MaxStepsResult{}, err
	}
	if in.N == nil {
		var n uint32
		if cfg := hd.Agent().Core.Snapshot().Header.Config; cfg != nil {
			n = cfg.Policy.MaxSteps
		}
		return MaxStepsResult{N: n}, nil
	}
	if err := hd.Do(host.Command{Kind: host.SetConfig, MaxSteps: in.N}); err != nil {
		return MaxStepsResult{}, wrap(err)
	}
	return MaxStepsResult{N: *in.N}, nil
}
