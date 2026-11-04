package session

// 内核回合状态机（M4 §5.1 / §5.2 T1–T20 / §5.3 S1–S7）。
//
// 约定：
//   - 全部命令要么完整生效、要么返回 error 且状态不变（S1）；
//   - Need 是内核对宿主的请求：Model（调模型）/ Tools（执行工具）/ Done（回合结束）；
//   - Change 告诉宿主发生了什么持久化可见的变更（先落库再发布由宿主保证，M6 R2）；
//   - 头部不存已收到的工具结果（R32）：结果条目按调用顺序落日志，
//     内核只接受「队首调用」的结果——乱序回传由宿主经 ToolResultStore 重排后再提交；
//   - 校验失败的调用不进执行队列：全无效时立即按序回填错误结果（T13），
//     混合时经 Need.Tools.Hints 让执行侧短路（保持 R14 调用顺序，且恢复安全）。

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// defaultClock / defaultIDs：默认时钟与 id 生成（测试经 WithClock / WithIDs 注入）。
func defaultClock() func() time.Time { return time.Now }

func defaultIDs() func() string {
	return func() string { id, _ := uuid.NewV7(); return id.String() }
}

// NeedKind：需求种类。
type NeedKind uint8

const (
	NeedModel NeedKind = iota + 1
	NeedTools
	NeedDone
)

// Need：内核需求（S2：ID 为递增 need_id，结果必须带对应编号）。
type Need struct {
	Kind  NeedKind
	ID    uint64
	Model *ModelNeed
	Tools *ToolsNeed
	Done  *DoneNeed
}

// ModelNeed：模型调用需求。
type ModelNeed struct {
	RequestID string // S7：对外标识，恢复后沿用；同 need 内的计划项由模型管理器以 attempt key（id#序号）区分；恢复时请求体哈希不符才换新 id
	Turn      uint32
	Step      uint32
}

// ToolsNeed：工具执行需求。
type ToolsNeed struct {
	Turn  uint32
	Calls []ToolCall        // 剩余未回结果的调用（按序，含校验失败的——执行侧按 Hints 短路）
	Hints map[string]string // callID → 校验失败原因（S3 立即回填的等价形态，保 R14 顺序）
}

// DoneNeed：回合结束。
type DoneNeed struct {
	Reason     EndReason
	Turn       uint32
	Unconsumed []PendingInput // T8：待处理插话原样交还
}

// ChangeKind：持久化可见的变更种类。
type ChangeKind uint8

const (
	ChangeAppended ChangeKind = iota + 1
	ChangeTruncated
	ChangeConfig
)

// Change：变更通知（宿主据此发布事件 / 同步镜像）。
type Change struct {
	Kind    ChangeKind
	From    int // Truncated：截断起点（完整日志下标）
	To      int // Appended：最后一条新条目的完整日志下标
	Entries int // Appended：追加条数
}

// Core：一个已加载会话的内核实例。
type Core struct {
	hdr      Header
	base     int
	items    []*Entry
	readOnly bool

	now   func() time.Time
	genID func() string
}

// Option：构造选项。
type Option func(*Core)

// WithClock 注入时钟（确定性测试）。
func WithClock(f func() time.Time) Option { return func(c *Core) { c.now = f } }

// WithIDs 注入 id 生成（确定性测试）。
func WithIDs(f func() string) Option { return func(c *Core) { c.genID = f } }

// New 新建会话内核（[*] → Idle）。
func New(cfg *Config, opts ...Option) *Core {
	c := &Core{
		hdr:   Header{Version: 1, Config: cfg, Status: StatusIdle, Ext: map[string]any{}},
		now:   time.Now,
		genID: func() string { id, _ := uuid.NewV7(); return id.String() },
	}
	for _, o := range opts {
		o(c)
	}
	if c.hdr.Ext == nil {
		c.hdr.Ext = map[string]any{}
	}
	return c
}

// ReadOnly 报告会话是否只读（存在未识别条目，R15）。
func (c *Core) ReadOnly() bool { return c.readOnly }

// Status 返回当前内核状态。
func (c *Core) Status() Status { return c.hdr.Status }

// Context 返回内核上下文（M4 §3.2；共享只读，不复制）。
func (c *Core) Context() Context {
	return Context{Config: c.hdr.Config, Items: c.items, Base: c.base, Ext: c.hdr.Ext}
}

// Snapshot 取快照（任何状态可用；共享底层数组，约定不可变）。
func (c *Core) Snapshot() Snapshot {
	return Snapshot{Header: c.hdr.clone(), Base: c.base, Entries: c.items}
}

// clone 复制头部（条目本体共享）。Ext 浅复制——键空间由宿主管理，值约定不可变。
func (h Header) clone() Header {
	cp := h
	if h.Ext != nil {
		cp.Ext = make(map[string]any, len(h.Ext))
		for k, v := range h.Ext {
			cp.Ext[k] = v
		}
	}
	cp.Config = h.Config // Config 约定不可变、按指针传递（M4 §3.2）
	return cp
}

// MarshalHeader 序列化头部（sessions.header 的 JSON）。
func MarshalHeader(h *Header) ([]byte, error) { return json.Marshal(h) }

// UnmarshalHeader 反序列化头部。
func UnmarshalHeader(b []byte) (Header, error) {
	var h Header
	if err := json.Unmarshal(b, &h); err != nil {
		return h, err
	}
	return h, nil
}

// —— 内部工具 ——//

func (c *Core) absLen() int { return c.base + len(c.items) }

func (c *Core) checkReadOnly() error {
	if c.readOnly {
		return ErrReadOnly
	}
	return nil
}

// metaSize 校验单条 meta 大小（S4：超限即拒不静默截断）。
func (c *Core) checkMetaSize(meta map[string]any) error {
	if meta == nil {
		return nil
	}
	if c.hdr.Config != nil && c.hdr.Config.Policy.MaxExtBytes > 0 {
		b, err := json.Marshal(meta)
		if err != nil {
			return ErrExtTooLarge
		}
		if uint64(len(b)) > c.hdr.Config.Policy.MaxExtBytes {
			return ErrExtTooLarge
		}
	}
	return nil
}

func (c *Core) checkExtSize() error {
	max := uint64(0)
	if c.hdr.Config != nil {
		max = c.hdr.Config.Policy.MaxExtBytes
	}
	if max == 0 {
		return nil
	}
	b, err := json.Marshal(c.hdr.Ext)
	if err != nil || uint64(len(b)) > max {
		return ErrExtTooLarge
	}
	return nil
}

// nextEntryID 分配条目 id 并推进 LastEntryID。
func (c *Core) nextEntryID() string {
	id := c.genID()
	c.hdr.LastEntryID = id
	return id
}

// lastTurn 返回最后一条条目的回合序号（无条目为 0）。
func (c *Core) lastTurn() uint32 {
	if len(c.items) == 0 {
		return 0
	}
	return c.items[len(c.items)-1].Turn
}

// turnHasAssistant 报告某回合是否已有 Assistant 条目。
func (c *Core) turnHasAssistant(turn uint32) bool {
	for _, e := range c.items {
		if e.Turn == turn && e.Item.Kind == KindAssistant {
			return true
		}
	}
	return false
}

// appendEntry 追加条目并推进 LogLen。
func (c *Core) appendEntry(turn uint32, meta map[string]any, it Item) *Entry {
	e := &Entry{V: 1, ID: c.nextEntryID(), Turn: turn, At: c.now().UnixMilli(), Meta: meta, Item: it}
	c.items = append(c.items, e)
	c.hdr.LogLen = c.absLen()
	return e
}

// emitNeed 分配 need_id 并构造需求（S2）。
func (c *Core) emitNeed(n *Need) *Need {
	c.hdr.NextNeedID++
	n.ID = c.hdr.NextNeedID
	return n
}

// newModelNeed 进入下一步：分配请求 id（S7）、推进步数（T19 判定在此之前的调用方完成）。
func (c *Core) newModelNeed(turn, step uint32) *Need {
	c.hdr.Status = StatusWaitingModel
	c.hdr.PendingModelRequestID = c.genID()
	return c.emitNeed(&Need{Kind: NeedModel, Model: &ModelNeed{RequestID: c.hdr.PendingModelRequestID, Turn: turn, Step: step}})
}

// newTurnNum 计算新回合序号（M4 §3.2 turn 语义：最后条目序号 + 1）——Record / Input 用。
func (c *Core) newTurnNum() uint32 {
	return c.lastTurn() + 1
}

// resumeTurnNum 恢复回合的序号：无 Assistant 条目的回合沿用原序号（resume 例外）。
func (c *Core) resumeTurnNum() uint32 {
	lt := c.lastTurn()
	if lt > 0 && !c.turnHasAssistant(lt) {
		return lt
	}
	return lt + 1
}

// endTurn 结束回合（T14 / T17 / T18 / T19 的公共尾部）。
func (c *Core) endTurn(reason EndReason, unconsumed []PendingInput) *Need {
	turn := uint32(0)
	if c.hdr.Turn != nil {
		turn = c.hdr.Turn.Turn
	}
	// 暂存压缩在回合结束时照样落条目（摘要已花钱生成；该步请求已按视图发出），upto 失效才丢弃。
	if pc := c.hdr.PendingCompact; pc != nil && c.hdr.Turn != nil && c.validateUpto(pc.Upto) == nil {
		c.appendEntry(c.hdr.Turn.Turn, pc.Meta, Item{Kind: KindCompaction,
			Compaction: &CompactionItem{Upto: pc.Upto, Summary: pc.Summary}})
		c.hdr.CompactionBase = pc.Upto
	}
	// 暂存配置在回合结束时照样生效：宿主已向调用方确认受理，不能因为回合先结束就丢掉。
	for _, p := range c.hdr.PendingConfig {
		_ = c.applyConfigPatch(p)
	}
	c.hdr.Status = StatusIdle
	c.hdr.Turn = nil
	c.hdr.PendingCalls = nil
	c.hdr.PendingInputs = nil
	c.hdr.PendingConfig = nil
	c.hdr.PendingCompact = nil
	c.hdr.Redirected = false
	c.hdr.PendingModelRequestID = ""
	return c.emitNeed(&Need{Kind: NeedDone, Done: &DoneNeed{Reason: reason, Turn: turn, Unconsumed: unconsumed}})
}

// flushCancelled 把未回传调用补成 Cancelled 条目（T7 / T17 / T18）。
func (c *Core) flushCancelled(st ToolStatus) []Change {
	var ch []Change
	for _, call := range c.hdr.PendingCalls {
		c.appendEntry(c.hdr.Turn.Turn, nil, Item{Kind: KindToolResult, ToolResult: &ToolResultItem{
			CallID: call.ID, Status: st,
		}})
		ch = append(ch, Change{Kind: ChangeAppended, To: c.absLen() - 1, Entries: 1})
	}
	c.hdr.PendingCalls = nil
	return ch
}

// stepBoundary 步边界（T20）：应用暂存配置 → 写暂存压缩条目 → 按到达顺序追加暂存插话
// → 进入下一步（Need::Model）或结束（MaxSteps，T19）。
func (c *Core) stepBoundary() ([]Change, *Need, error) {
	var ch []Change
	for range c.hdr.PendingConfig {
		if err := c.applyConfigPatch(c.hdr.PendingConfig[len(ch)]); err != nil {
			return nil, nil, err
		}
		ch = append(ch, Change{Kind: ChangeConfig})
	}
	c.hdr.PendingConfig = nil

	if pc := c.hdr.PendingCompact; pc != nil {
		if err := c.validateUpto(pc.Upto); err != nil {
			return nil, nil, err
		}
		c.appendEntry(c.hdr.Turn.Turn, pc.Meta, Item{Kind: KindCompaction,
			Compaction: &CompactionItem{Upto: pc.Upto, Summary: pc.Summary}})
		c.hdr.CompactionBase = pc.Upto
		ch = append(ch, Change{Kind: ChangeAppended, To: c.absLen() - 1, Entries: 1})
		c.hdr.PendingCompact = nil
	}

	for _, pi := range c.hdr.PendingInputs {
		c.appendEntry(c.hdr.Turn.Turn, pi.Meta, Item{Kind: KindUser, User: &UserItem{Content: pi.Content}})
		ch = append(ch, Change{Kind: ChangeAppended, To: c.absLen() - 1, Entries: 1})
	}
	c.hdr.PendingInputs = nil
	c.hdr.Redirected = false

	// T19：进入下一步时步数达到 max_steps → Done(MaxSteps)。0 表示不限（缺省；交互使用由人中断）。
	if c.hdr.Config != nil && c.hdr.Config.Policy.MaxSteps > 0 && c.hdr.Turn.Step >= c.hdr.Config.Policy.MaxSteps {
		return ch, c.endTurn(MaxStepsReason(), nil), nil
	}
	c.hdr.Turn.Step++
	return ch, c.newModelNeed(c.hdr.Turn.Turn, c.hdr.Turn.Step), nil
}

func (c *Core) applyConfigPatch(p ConfigPatch) error {
	if c.hdr.Config == nil {
		return nil
	}
	cp := *c.hdr.Config
	if p.Model != nil {
		cp.Model = *p.Model
	}
	if p.Params != nil {
		cp.Params = *p.Params
	}
	if p.MaxSteps != nil {
		cp.Policy.MaxSteps = *p.MaxSteps
	}
	// AddTools：显露集只增不减、按名去重（tool-hub D1；只对 tool-hub 开放由宿主白名单强制）。
	if len(p.AddTools) > 0 {
		have := make(map[string]bool, len(cp.Tools))
		for _, t := range cp.Tools {
			have[t.Name] = true
		}
		tools := append([]ToolSpec(nil), cp.Tools...)
		for _, t := range p.AddTools {
			if !have[t.Name] {
				have[t.Name] = true
				tools = append(tools, t)
			}
		}
		cp.Tools = tools
	}
	c.hdr.Config = &cp
	return nil
}

// validateUpto 校验压缩边界：不拆开调用与结果（R14），且不越过当前末尾、不回退。
// CanCompact upto 是否是合法的压缩边界（压缩插件选切点用）。
func (c *Core) CanCompact(upto int) bool { return c.validateUpto(upto) == nil }

func (c *Core) validateUpto(upto int) error {
	if upto < c.hdr.CompactionBase || upto > c.absLen() {
		return ErrBadRewind
	}
	// upto 处于某 assistant 条目的调用-结果区间内 → 拒绝。
	// 步边界（该步调用的结果全部到齐）与回合起点都合法（E34）。
	off := upto - c.base
	open := 0
	for i := 0; i < off && i < len(c.items); i++ {
		it := c.items[i].Item
		switch it.Kind {
		case KindAssistant:
			open = len(it.Assistant.ToolCalls)
		case KindToolResult:
			if open > 0 {
				open--
			}
		}
		// user / 注入条目不闭合调用：结果没到齐之前出现的任何条目都不能当作切点（R14）。
	}
	if open > 0 {
		return ErrBadRewind
	}
	return nil
}

// —— 命令：Idle 专属（T1 / T2 / T3 / T4）——//

// Input 开启回合（T1）：追加 User 条目 → run_state running 由宿主写 → 发出 Need::Model。
func (c *Core) Input(content Content, meta map[string]any) ([]Change, *Need, error) {
	if err := c.checkReadOnly(); err != nil {
		return nil, nil, err
	}
	if c.hdr.Status != StatusIdle {
		return nil, nil, ErrNotIdle
	}
	if err := c.checkMetaSize(meta); err != nil {
		return nil, nil, err
	}
	turn := c.newTurnNum()
	c.appendEntry(turn, meta, Item{Kind: KindUser, User: &UserItem{Content: content}})
	ch := []Change{{Kind: ChangeAppended, To: c.absLen() - 1, Entries: 1}}
	c.hdr.Turn = &TurnInfo{Turn: turn, Start: c.absLen() - 1, Step: 1}
	need := c.newModelNeed(turn, 1)
	return ch, need, nil
}

// Record 只记录（T2）：追加 User 条目、占新回合序号（零步回合），不发出需求。
func (c *Core) Record(content Content, meta map[string]any) ([]Change, error) {
	if err := c.checkReadOnly(); err != nil {
		return nil, err
	}
	if c.hdr.Status != StatusIdle {
		return nil, ErrNotIdle
	}
	if err := c.checkMetaSize(meta); err != nil {
		return nil, err
	}
	turn := c.newTurnNum() // T2：record 占新回合序号（零步回合）
	c.appendEntry(turn, meta, Item{Kind: KindUser, User: &UserItem{Content: content}})
	return []Change{{Kind: ChangeAppended, To: c.absLen() - 1, Entries: 1}}, nil
}

// Resume 恢复回合（T1 resume 变体：失败重试 / 步数上限后继续）。
// 末尾必须是 User / ToolResult 条目；无 Assistant 条目的回合沿用原序号。
func (c *Core) Resume() ([]Change, *Need, error) {
	if err := c.checkReadOnly(); err != nil {
		return nil, nil, err
	}
	if c.hdr.Status != StatusIdle {
		return nil, nil, ErrNotIdle
	}
	if len(c.items) == 0 {
		return nil, nil, ErrNoTurn
	}
	last := c.items[len(c.items)-1]
	if k := last.Item.Kind; k != KindUser && k != KindToolResult {
		return nil, nil, ErrBadResume
	}
	turn := c.resumeTurnNum()
	start := c.absLen()
	if turn == last.Turn { // 沿用原回合：起点回到该回合第一条
		for i, e := range c.items {
			if e.Turn == turn {
				start = c.base + i
				break
			}
		}
	}
	c.hdr.Turn = &TurnInfo{Turn: turn, Start: start, Step: 1}
	return nil, c.newModelNeed(turn, 1), nil
}

// Rewind 截断（T4）：to 是某回合首条 User 条目的下标，截到 [0, to)——这一回合及之后的全部去掉。
// to == base（0 或压缩点）、to == 日志长度（不截）也合法。截掉压缩条目时起点退回上一次压缩。
func (c *Core) Rewind(to int) ([]Change, error) {
	if err := c.checkReadOnly(); err != nil {
		return nil, err
	}
	if c.hdr.Status != StatusIdle {
		return nil, ErrNotIdle
	}
	if to < c.base || to > c.absLen() {
		return nil, ErrBadRewind
	}
	if to > c.base && to < c.absLen() {
		off := to - c.base
		if c.items[off].Item.Kind != KindUser || // 回合起点 = 该回合的首条 User 条目
			c.items[off-1].Turn == c.items[off].Turn {
			return nil, ErrBadRewind
		}
	}
	// 截掉压缩条目 → 退回 [base, to) 内最后一次压缩。判断看压缩条目本身的位置，不看它的 upto：
	// 压缩条目写在 upto 之后的任意位置，upto < to 时条目照样可能被截掉。
	overCompaction := c.hdr.CompactionBase >= to && c.hdr.CompactionBase > 0
	for i := to - c.base; i < len(c.items); i++ {
		if c.items[i].Item.Kind == KindCompaction {
			overCompaction = true
			break
		}
	}
	if overCompaction {
		prev := 0
		found := false
		for i := 0; i < to-c.base && i < len(c.items); i++ {
			if it := c.items[i].Item; it.Kind == KindCompaction {
				prev = it.Compaction.Upto
				found = true
			}
		}
		if !found && c.base != 0 {
			return nil, ErrBadRewind // 更早的压缩点不在加载窗口内，宿主需从 0 重读
		}
		c.hdr.CompactionBase = prev
	}
	c.items = c.items[:to-c.base]
	c.hdr.LogLen = c.absLen()
	if c.hdr.LogLen == 0 {
		c.hdr.LastEntryID = ""
	}
	return []Change{{Kind: ChangeTruncated, From: to}}, nil
}

// —— 命令：任意状态 ——//

// SetConfig 配置修改（T5）：Idle 立即生效，等待态暂存到步边界。
// R23 字段白名单（外部只能改 model / params / max_steps）由宿主强制。
func (c *Core) SetConfig(patch ConfigPatch) ([]Change, *Need, error) {
	if err := c.checkReadOnly(); err != nil {
		return nil, nil, err
	}
	if c.hdr.Status == StatusIdle {
		if err := c.applyConfigPatch(patch); err != nil {
			return nil, nil, err
		}
		return []Change{{Kind: ChangeConfig}}, nil, nil
	}
	c.hdr.PendingConfig = append(c.hdr.PendingConfig, patch)
	return nil, nil, nil
}

// SetExt 写扩展（任何状态立即生效；停顿点之外修改到下一次 checkpoint 落库——宿主职责）。
// rt.* / plugin.* 命名空间由宿主强制，内核只管大小上限（S4）。
func (c *Core) SetExt(k string, v any) error {
	if err := c.checkReadOnly(); err != nil {
		return err
	}
	if c.hdr.Ext == nil {
		c.hdr.Ext = map[string]any{}
	}
	old, had := c.hdr.Ext[k]
	if v == nil {
		delete(c.hdr.Ext, k)
	} else {
		c.hdr.Ext[k] = v
	}
	if err := c.checkExtSize(); err != nil { // 超限：恢复原值（S1：要么完整生效，要么不变）
		if had {
			c.hdr.Ext[k] = old
		} else {
			delete(c.hdr.Ext, k)
		}
		return err
	}
	return nil
}

// Compact 压缩（T3 / T5）：Idle 直接追加压缩条目；等待态暂存到步边界。
func (c *Core) Compact(summary Content, upto int, meta map[string]any) ([]Change, error) {
	if err := c.checkReadOnly(); err != nil {
		return nil, err
	}
	if err := c.checkMetaSize(meta); err != nil {
		return nil, err
	}
	if err := c.validateUpto(upto); err != nil {
		return nil, err
	}
	if c.hdr.Status == StatusIdle {
		c.appendEntry(c.lastTurn(), meta, Item{Kind: KindCompaction, Compaction: &CompactionItem{Upto: upto, Summary: summary}})
		c.hdr.CompactionBase = upto
		return []Change{{Kind: ChangeAppended, To: c.absLen() - 1, Entries: 1}}, nil
	}
	c.hdr.PendingCompact = &PendingCompact{Summary: summary, Upto: upto, Meta: meta}
	return nil, nil
}

// Inject 追加一条上下文注入条目（框架设计 12.5）：kind 为 snapshot / status；
// text 是内核渲染好的注入文本，meta 记录已发哈希（条目只读、随检查点持久化）。
// 注入只追加条目、不改回合状态：可在 WaitingModel / WaitingTools 下调用。
func (c *Core) Inject(kind string, text string, meta map[string]any) ([]Change, error) {
	if err := c.checkReadOnly(); err != nil {
		return nil, err
	}
	if kind != KindSnapshot && kind != KindStatus {
		return nil, fmt.Errorf("%w: 注入种类 %q", ErrState, kind)
	}
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	if c.hdr.Status == StatusWaitingTools {
		return nil, ErrToolsOpen // 注入要等本步结果到齐（步边界之后）
	}
	if err := c.checkMetaSize(meta); err != nil {
		return nil, err
	}
	c.appendEntry(c.lastTurn(), meta, Item{Kind: kind, Injection: &InjectionItem{Text: text}})
	return []Change{{Kind: ChangeAppended, To: c.absLen() - 1, Entries: 1}}, nil
}

// —— 命令：等待态专属（T5 / T6 / T7 / T17 / T18）——//

// Steer 插话（等待态）：Append 暂存到步边界；Redirect 标记改向（T6 / T7）。
func (c *Core) Steer(content Content, meta map[string]any, mode SteerMode) ([]Change, *Need, error) {
	if err := c.checkReadOnly(); err != nil {
		return nil, nil, err
	}
	if c.hdr.Status == StatusIdle {
		return nil, nil, ErrNotWaiting
	}
	if err := c.checkMetaSize(meta); err != nil {
		return nil, nil, err
	}
	if meta == nil {
		meta = map[string]any{}
	}
	meta[MetaSteer] = string(mode)

	if mode == SteerAppend {
		c.hdr.PendingInputs = append(c.hdr.PendingInputs, PendingInput{Content: content, Meta: meta, Mode: mode})
		return nil, nil, nil
	}
	// Redirect
	if c.hdr.Status == StatusWaitingModel {
		c.hdr.Redirected = true // T6：回复到达后有效调用补 Cancelled(Redirected)
		c.hdr.PendingInputs = append(c.hdr.PendingInputs, PendingInput{Content: content, Meta: meta, Mode: mode})
		return nil, nil, nil
	}
	// T7：WaitingTools —— 未回传调用立即补 Cancelled(Redirected)，立即进入步边界。
	ch := c.flushCancelled(ToolRedirected)
	c.hdr.PendingInputs = append(c.hdr.PendingInputs, PendingInput{Content: content, Meta: meta, Mode: mode})
	ch2, need, err := c.stepBoundary()
	return append(ch, ch2...), need, err
}

// MidInput 在回合进行中立即追加一条用户输入条目（09 9.4：运行中到达的消息在下一次调模型前插入）。
// 与 Steer(Append) 不同，它不等步边界，直接进入下一次请求的历史；回合结束前仍属本回合。
func (c *Core) MidInput(content Content, meta map[string]any) ([]Change, error) {
	if err := c.checkReadOnly(); err != nil {
		return nil, err
	}
	if c.hdr.Status == StatusIdle {
		return nil, ErrNotWaiting
	}
	if c.hdr.Status == StatusWaitingTools {
		return nil, ErrToolsOpen // 插在调用与结果之间会让下一次请求非法：等步边界
	}
	if err := c.checkMetaSize(meta); err != nil {
		return nil, err
	}
	if meta == nil {
		meta = map[string]any{}
	}
	if c.hdr.Turn == nil {
		return nil, ErrNotWaiting
	}
	c.appendEntry(c.hdr.Turn.Turn, meta, Item{Kind: KindUser, User: &UserItem{Content: content}})
	return []Change{{Kind: ChangeAppended, To: c.absLen() - 1, Entries: 1}}, nil
}

// Cancel 取消（T17）：先 flush 暂存项；未回传调用补 Cancelled(TurnCancelled)；Done(Cancelled)。
func (c *Core) Cancel() ([]Change, *Need, error) {
	if err := c.checkReadOnly(); err != nil {
		return nil, nil, err
	}
	if c.hdr.Status == StatusIdle {
		return nil, nil, ErrNotWaiting
	}
	var ch []Change
	if c.hdr.Status == StatusWaitingTools {
		ch = c.flushCancelled(ToolTurnCancelled)
	}
	// T17：未消费的插话（含改向输入）原样交还，不随回合结束丢失（03 §6.3）。
	return ch, c.endTurn(CancelledReason(), c.takePendingInputs()), nil
}

// Abort 直接结束回合（T18）：宿主在恢复循环等场景使用。
func (c *Core) Abort(reason EndReason) ([]Change, *Need, error) {
	if err := c.checkReadOnly(); err != nil {
		return nil, nil, err
	}
	if c.hdr.Status == StatusIdle {
		return nil, nil, ErrNotWaiting
	}
	var ch []Change
	if c.hdr.Status == StatusWaitingTools {
		ch = c.flushCancelled(ToolTurnCancelled)
	}
	return ch, c.endTurn(reason, c.takePendingInputs()), nil
}

// takePendingInputs 取出暂存插话（提前结束时交还给调用方）。
func (c *Core) takePendingInputs() []PendingInput {
	if len(c.hdr.PendingInputs) == 0 {
		return nil
	}
	return append([]PendingInput(nil), c.hdr.PendingInputs...)
}

// —— 工具结果（T15 / T16 / T20 / S2 / S6）——//

// SubmitResult 提交工具结果：只接受队首调用（乱序由宿主重排后提交）；
// 结果大小超限替换为错误结果（S4）；写完最后一条进入步边界（T16 / T20）。
func (c *Core) SubmitResult(callID string, out Content, st ToolStatus) ([]Change, *Need, error) {
	if err := c.checkReadOnly(); err != nil {
		return nil, nil, err
	}
	if c.hdr.Status != StatusWaitingTools {
		return nil, nil, ErrNotWaiting
	}
	if len(c.hdr.PendingCalls) == 0 || c.hdr.PendingCalls[0].ID != callID {
		return nil, nil, errUnknownCall("call " + callID + " 不是队首调用或已回传")
	}
	// S4：结果超限替换为错误结果回给模型（不静默截断）。
	if c.hdr.Config != nil && c.hdr.Config.Policy.MaxResultBytes > 0 && contentSize(out) > c.hdr.Config.Policy.MaxResultBytes {
		out, st = Content{NewTextPart("工具结果超过大小上限，请缩小范围或分页获取")}, ToolError
	}
	c.appendEntry(c.hdr.Turn.Turn, nil, Item{Kind: KindToolResult, ToolResult: &ToolResultItem{
		CallID: callID, Output: out, Status: st,
	}})
	ch := []Change{{Kind: ChangeAppended, To: c.absLen() - 1, Entries: 1}}
	c.hdr.PendingCalls = c.hdr.PendingCalls[1:]
	if len(c.hdr.PendingCalls) > 0 { // T15：部分结果
		return ch, c.emitNeed(&Need{Kind: NeedTools, Tools: &ToolsNeed{
			Turn: c.hdr.Turn.Turn, Calls: append([]ToolCall(nil), c.hdr.PendingCalls...)}}), nil
	}
	// T16 / T20：全部到齐 → 步边界。
	ch2, need, err := c.stepBoundary()
	return append(ch, ch2...), need, err
}

// —— 模型事实（T8–T14）——//

// ModelDone 模型回复到达（WaitingModel）。requestID / needID 不符 → Stale（S2 / S7）。
func (c *Core) ModelDone(requestID string, needID uint64, reply AssistantItem, stop StopReason) ([]Change, *Need, error) {
	if err := c.checkReadOnly(); err != nil {
		return nil, nil, err
	}
	if c.hdr.Status != StatusWaitingModel {
		return nil, nil, ErrNotWaiting
	}
	if requestID != c.hdr.PendingModelRequestID || needID != c.hdr.NextNeedID {
		return nil, nil, errStale("模型结果与当前需求不匹配")
	}
	if c.hdr.Turn != nil {
		c.hdr.Turn.Usage.Input += reply.Usage.Input
		c.hdr.Turn.Usage.Output += reply.Usage.Output
		c.hdr.Turn.Usage.CacheRead += reply.Usage.CacheRead
		c.hdr.Turn.Usage.CacheWrite += reply.Usage.CacheWrite
	}

	// 为调用分配内核 id（S6），wire_id 存厂商原始 id；不合法的参数 JSON 先规整（见 normalizeCalls）。
	c.normalizeCalls(reply.ToolCalls)

	redirected := c.hdr.Redirected
	empty := len(reply.Content) == 0 && len(reply.ToolCalls) == 0

	if empty { // T8：空回复不写条目；待处理插话原样交还
		unconsumed := append([]PendingInput(nil), c.hdr.PendingInputs...)
		return nil, c.endTurn(FailedReason("empty response"), unconsumed), nil
	}

	// 追加 Assistant 条目（调用即使被改向也原样记录，R14 由 Cancelled 结果闭合）。
	c.appendEntry(c.hdr.Turn.Turn, nil, Item{Kind: KindAssistant, Assistant: &reply})
	ch := []Change{{Kind: ChangeAppended, To: c.absLen() - 1, Entries: 1}}

	if redirected && len(reply.ToolCalls) > 0 { // T11：有效调用补 Cancelled(Redirected) → 步边界
		for _, call := range reply.ToolCalls {
			c.appendEntry(c.hdr.Turn.Turn, nil, Item{Kind: KindToolResult, ToolResult: &ToolResultItem{
				CallID: call.ID, Status: ToolRedirected}})
			ch = append(ch, Change{Kind: ChangeAppended, To: c.absLen() - 1, Entries: 1})
		}
		c.hdr.PendingCalls = nil
		ch2, need, err := c.stepBoundary()
		return append(ch, ch2...), need, err
	}

	if len(reply.ToolCalls) == 0 {
		if len(c.hdr.PendingInputs) > 0 { // T10：有待处理插话 → 步边界（追加插话）后下一步
			ch2, need, err := c.stepBoundary()
			return append(ch, ch2...), need, err
		}
		_ = stop // 截断与否由宿主展示；回合正常完成
		return ch, c.endTurn(DoneReason(), nil), nil
	}

	// T12 / T13：校验调用（S3）。
	hints, allInvalid := c.validateCalls(reply.ToolCalls)
	c.hdr.PendingCalls = append([]ToolCall(nil), reply.ToolCalls...)
	c.hdr.Status = StatusWaitingTools
	if allInvalid { // T13：全部无效 → 立即按序回填错误结果（顺序安全：无有效调用穿插）
		for _, call := range reply.ToolCalls {
			c.appendEntry(c.hdr.Turn.Turn, nil, Item{Kind: KindToolResult, ToolResult: &ToolResultItem{
				CallID: call.ID, Status: ToolError,
				Output: Content{NewTextPart(hints[call.ID])}}})
			ch = append(ch, Change{Kind: ChangeAppended, To: c.absLen() - 1, Entries: 1})
		}
		c.hdr.PendingCalls = nil
		ch2, need, err := c.stepBoundary()
		return append(ch, ch2...), need, err
	}
	// T12：有效调用 → Need::Tools（校验失败的由执行侧按 Hints 短路回填，保 R14 顺序）。
	return ch, c.emitNeed(&Need{Kind: NeedTools, Tools: &ToolsNeed{
		Turn: c.hdr.Turn.Turn, Calls: append([]ToolCall(nil), c.hdr.PendingCalls...), Hints: hints}}), nil
}

// ModelInterrupted 模型流被中断（T14）：正文非空写 interrupted 条目（调用补 Cancelled
// 保 R14），以调用方给定的原因结束回合。
func (c *Core) ModelInterrupted(requestID string, needID uint64, reply AssistantItem, reason EndReason) ([]Change, *Need, error) {
	if err := c.checkReadOnly(); err != nil {
		return nil, nil, err
	}
	if c.hdr.Status != StatusWaitingModel {
		return nil, nil, ErrNotWaiting
	}
	if requestID != c.hdr.PendingModelRequestID || needID != c.hdr.NextNeedID {
		return nil, nil, errStale("模型结果与当前需求不匹配")
	}
	var ch []Change
	if len(reply.Content) > 0 || len(reply.ToolCalls) > 0 {
		c.normalizeCalls(reply.ToolCalls) // 中断时参数常是半截 JSON
		reply.Interrupted = true
		c.appendEntry(c.hdr.Turn.Turn, nil, Item{Kind: KindAssistant, Assistant: &reply})
		ch = append(ch, Change{Kind: ChangeAppended, To: c.absLen() - 1, Entries: 1})
		for _, call := range reply.ToolCalls { // 被中断的调用不执行，补 Cancelled 闭合 R14
			c.appendEntry(c.hdr.Turn.Turn, nil, Item{Kind: KindToolResult, ToolResult: &ToolResultItem{
				CallID: call.ID, Status: ToolTurnCancelled}})
			ch = append(ch, Change{Kind: ChangeAppended, To: c.absLen() - 1, Entries: 1})
		}
	}
	return ch, c.endTurn(reason, nil), nil
}

// ModelFailed 模型调用失败（T14）：不写条目，以 Failed / Denied 结束。
func (c *Core) ModelFailed(requestID string, needID uint64, reason EndReason) ([]Change, *Need, error) {
	if err := c.checkReadOnly(); err != nil {
		return nil, nil, err
	}
	if c.hdr.Status != StatusWaitingModel {
		return nil, nil, ErrNotWaiting
	}
	if requestID != c.hdr.PendingModelRequestID || needID != c.hdr.NextNeedID {
		return nil, nil, errStale("模型结果与当前需求不匹配")
	}
	return nil, c.endTurn(reason, nil), nil
}

// validateCalls 校验模型请求的工具调用（S3）：工具名、参数 JSON、单步调用数。
// 返回 callID → 错误提示；allInvalid 报告是否全部无效（T13 立即回填路径）。
// InvalidArgsKey 不合法参数 JSON 规整后保存原始文本的键（见 normalizeCalls）。
const InvalidArgsKey = "_invalid_args"

// normalizeCalls 分配内核 id 与 wire_id，并把不合法的参数 JSON 换成 `{"_invalid_args": "<原文>"}`：
// 原样存进条目会让条目无法序列化（之后每个检查点都失败），厂商也不接受非对象的调用参数。
// 标记留在参数里随条目持久化，恢复后 validateCalls 仍能认出它、不会把调用当作 `{}` 执行。
func (c *Core) normalizeCalls(calls []ToolCall) {
	used := c.usedWireIDs()
	for i := range calls {
		if calls[i].ID == "" {
			calls[i].ID = c.genID()
		}
		// 厂商 id 缺省或已用过（同一回复里重复、或历史里出现过——有的兼容实现每轮都从 call_0 起编）
		// 时改用内核 id：一个请求里 tool_use id 必须唯一，Restore 也据此校验。
		if calls[i].WireID == "" || used[calls[i].WireID] {
			calls[i].WireID = calls[i].ID
		}
		used[calls[i].WireID] = true
		if a := calls[i].Args; len(a) > 0 && !json.Valid(a) {
			b, _ := json.Marshal(map[string]string{InvalidArgsKey: string(a)})
			calls[i].Args = b
		}
	}
}

// usedWireIDs 加载窗口里已用过的厂商调用 id。
func (c *Core) usedWireIDs() map[string]bool {
	used := map[string]bool{}
	for _, e := range c.items {
		if a := e.Item.Assistant; e.Item.Kind == KindAssistant && a != nil {
			for _, tc := range a.ToolCalls {
				if tc.WireID != "" {
					used[tc.WireID] = true
				}
			}
		}
	}
	return used
}

// invalidArgs 参数是否是 normalizeCalls 规整过的非法 JSON。
func invalidArgs(a json.RawMessage) bool {
	if !json.Valid(a) {
		return true
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(a, &m) != nil || len(m) != 1 {
		return false
	}
	_, ok := m[InvalidArgsKey]
	return ok
}

func (c *Core) validateCalls(calls []ToolCall) (map[string]string, bool) {
	hints := map[string]string{}
	known := map[string]bool{}
	if c.hdr.Config != nil {
		for _, t := range c.hdr.Config.Tools {
			known[t.Name] = true
		}
	}
	limit := int(c.hdr.Config.Policy.MaxToolCallsPerStep)
	if limit <= 0 {
		limit = 16
	}
	allInvalid := true
	for i, call := range calls {
		switch {
		case i >= limit:
			hints[call.ID] = "单步最多 " + itoa(limit) + " 个工具调用，请减少并行调用数量"
		case !known[call.Name]:
			hints[call.ID] = "工具 " + call.Name + " 不存在，可用工具：" + c.toolNames()
		case invalidArgs(call.Args):
			hints[call.ID] = "工具 " + call.Name + " 的参数不是合法 JSON，请修正后重试"
		default:
			allInvalid = false
		}
	}
	return hints, allInvalid
}

func (c *Core) toolNames() string {
	s := ""
	if c.hdr.Config != nil {
		for i, t := range c.hdr.Config.Tools {
			if i > 0 {
				s += ", "
			}
			s += t.Name
		}
	}
	return s
}

func contentSize(ct Content) uint64 {
	b, err := json.Marshal(ct)
	if err != nil {
		return 0
	}
	return uint64(len(b))
}

func itoa(i int) string { return strconv.Itoa(i) }
