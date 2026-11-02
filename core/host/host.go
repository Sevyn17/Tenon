// Package host：会话宿主（session-host 定稿 D1–D6，M4.1–4.5）。
//
// 每个已打开的会话一个 actor（goroutine），独占内核（内核非并发安全）。
//   - 外部命令走有界队列（满 → ErrBusy）；Cancel 与 Confirm 立即生效（不排队等回合结束）。
//   - 主循环只处理 Idle；回合进行中投递的插话 / 配置 / 压缩在需求边界应用（agent.Between）。
//   - Send / Note 先落 inbox 再确认，item id 幂等（D2 / D6）；回合开始时消费 inbox：note 在前、input 在后，trust 取最低。
//   - 人工确认单一路径（D3）：Guard → Confirm → 挂起记录 → HumanRequested → Confirm 命令 → 原 id 继续。
//   - actor 内 panic 被捕获：不写头部，下次打开从最后一次 checkpoint 恢复。
package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"ybase-kernel/core/agent"
	"ybase-kernel/core/bus"
	"ybase-kernel/core/session"
	"ybase-kernel/core/store"
	"ybase-kernel/core/toolhub"
)

// 错误。
var (
	ErrBusy   = errors.New("host: 命令队列已满")
	ErrClosed = errors.New("host: 会话已关闭")
)

// Kind 命令种类（session-host D1 全集；op_id 去重推迟）。
type Kind string

const (
	Send    Kind = "send"
	Note    Kind = "note"
	Steer   Kind = "steer"
	Resume  Kind = "resume"
	Cancel  Kind = "cancel"
	Confirm Kind = "confirm"
	Record  Kind = "record"
	SetExt  Kind = "set_ext"
	// SetConfig 改会话配置（外部白名单：只开放 MaxSteps）；Idle 立即生效，回合中到步边界生效。
	SetConfig Kind = "set_config"
	Compact   Kind = "compact"
	Rewind    Kind = "rewind"
	Close     Kind = "close"
	Drain     Kind = "drain"
)

// Command 一条命令。
type Command struct {
	Kind     Kind
	ItemID   string // Send / Note：调用方生成，幂等键
	Text     string
	Trust    string // trusted / untrusted
	Source   string // user / agent / system …
	Mode     session.SteerMode
	CallID   string  // Confirm
	Approve  bool    // Confirm
	To       int     // Rewind
	Upto     int     // Compact
	Key      string  // SetExt
	Value    any     // SetExt
	MaxSteps *uint32 // SetConfig：单回合步数上限，0 = 不限
	done     chan error
}

// Decision before_turn 的结论。
type Decision int

const (
	Allow   Decision = iota
	Deny             // 不开回合，输入交还
	RecordD          // 只记录，不开回合（转人工）
)

// Origin 会话来源（plugin-manager L2）。
type Origin struct {
	Kind     string // new / fork / side / subagent / member
	ParentID string
	ForkUpto int
}

// Hooks 会话钩子（plugin-manager §5.4）。均可空。
type Hooks struct {
	OnCreate   func(id string, o Origin) error // 失败即回滚创建
	OnRestore  func(id string) error           // 失败即拒绝打开
	BeforeTurn func(id string, in session.Content, untrusted bool) (session.Content, Decision, error)
	AfterTurn  func(id string, end session.EndReason)
}

// Factory 为会话构造 agent（模型、工具、存储、显示回调已配好）与新会话配置。
//
// def 是会话行里记的智能体定义名（T1b-2c：子智能体、工作组成员各自有内置定义；
// 空 = 无定义会话，如用户直接新建的会话）。
type Factory func(id, def string) (*agent.Agent, *session.Config, error)

// Store 宿主用到的存储能力：会话 + 写租约 + 输入队列 + 休眠状态。
type Store interface {
	store.SessionStore
	store.LeaseStore
	store.SleepStore
	store.InboxStore
}

// Host 进程内会话表。
type Host struct {
	DB    Store
	New   Factory
	Child Factory // 子智能体会话的构造（可空 = New）；通常去掉委派工具以限制深度
	Hooks Hooks
	Bus   *bus.Bus // 可空：易失事件（界面）
	Queue int      // 外部队列容量，默认 64

	// Holder 本实例的租约持有者标识（空 = 首次打开时按实例生成）。
	// 同一存储上两个 Host 打开同一会话会互相抢租约，旧持有者的写入被存储拒绝（02 2.2 fencing）。
	Holder string
	// Limit 同时推理的会话数上限（0 = 不限）；超出上限的回合排队等待，不报错（02 2.4）。
	Limit int
	// SleepAfter 空闲多久后休眠（0 = 不休眠）。休眠的会话释放进程内对象与写租约，
	// 只剩存储里的几行；有新信件时任一节点抢到租约即可唤醒（02 2.2）。
	SleepAfter time.Duration
	// TreePolicy 所属树策略（02 2.2）：子会话一轮运行失败时 restart | give_up | escalate；
	// 空/未知按 give_up（标记失败并告知父会话——当前实现是 actor 退出、错误原样交回调用方）。
	TreePolicy string
	// TreeRestarts restart 策略下的最大重跑次数（<=0 按 1）。
	TreeRestarts int

	mu     sync.Mutex
	actors map[string]*actor
	sem    chan struct{} // Limit 的许可池，首次使用时按 Limit 建
}

// Handle 会话句柄：只投递命令。
type Handle struct{ a *actor }

// ID 会话 id。
func (h Handle) ID() string { return h.a.id }

// Agent 底层运行体（只读查询用；不要从 actor 外调用其方法改状态）。
func (h Handle) Agent() *agent.Agent { return h.a.ag }

// Do 投递命令并等待它被受理（Send / Note 返回时已落 inbox；Idle 命令返回时已提交）。
// 回合中投递的 Send 排到下一回合；返回不等待回合结束。
func (h Handle) Do(c Command) error {
	c.done = make(chan error, 1)
	if err := h.a.enqueue(c); err != nil {
		return err
	}
	select {
	case err := <-c.done:
		return err
	case <-h.a.exit:
		return ErrClosed
	}
}

// Wait 等到会话空闲（无进行中回合且 inbox 已处理）。测试与 CLI 用。
func (h Handle) Wait() error {
	done := make(chan error, 1)
	if err := h.a.enqueue(Command{Kind: "barrier", done: done}); err != nil {
		if errors.Is(err, ErrClosed) {
			<-h.a.exit
			if h.a.exitErr != nil {
				return h.a.exitErr // 已退出：报告退出原因而非笼统的"已关闭"
			}
		}
		return err
	}
	select {
	case err := <-done:
		return err
	case <-h.a.exit:
		return h.a.exitErr
	}
}

// Done 会话 actor 退出时关闭。
func (h Handle) Done() <-chan struct{} { return h.a.exit }

// Err actor 退出原因。
func (h Handle) Err() error { return h.a.exitErr }

// Open 打开（不存在则新建，origin = new）。已打开的返回同一句柄。
func (h *Host) Open(ctx context.Context, id string) (Handle, error) {
	return h.open(ctx, id, "", h.New, nil)
}

// open 打开会话；def 是要用的智能体定义名（空 = 无定义，恢复已有会话时从会话行取）；
// pre 非空时代替默认的 Open（调用方自己建会话行，如子智能体）。
func (h *Host) open(ctx context.Context, id, def string, f Factory, pre func(*agent.Agent, *session.Config) error) (Handle, error) {
	h.mu.Lock()
	if h.actors == nil {
		h.actors = map[string]*actor{}
	}
	if a, ok := h.actors[id]; ok {
		h.mu.Unlock()
		return Handle{a}, nil
	}
	h.mu.Unlock()

	ag, cfg, err := f(id, def)
	if err != nil {
		return Handle{}, err
	}
	a := &actor{h: h, id: id, ag: ag, in: make(chan Command, h.queue()), exit: make(chan struct{}),
		waiters: map[string]chan bool{}}
	ag.Between = a.between
	if ag.Hub != nil {
		a.wireConfirm()
	}
	existed, err := h.DB.Exists(ctx, id)
	if err != nil {
		return Handle{}, err
	}
	if pre != nil {
		if err := pre(ag, cfg); err != nil {
			return Handle{}, err
		}
	} else if _, err := ag.Open(ctx, cfg); err != nil {
		return Handle{}, err
	}
	if !existed && pre == nil {
		if err := h.created(ctx, id, Origin{Kind: "new"}); err != nil {
			return Handle{}, err
		}
	}
	if existed && h.Hooks.OnRestore != nil {
		if err := h.Hooks.OnRestore(id); err != nil {
			return Handle{}, fmt.Errorf("restore 钩子拒绝: %w", err)
		}
	}
	h.mu.Lock()
	if b, ok := h.actors[id]; ok { // 并发打开：以先到者为准
		h.mu.Unlock()
		return Handle{b}, nil
	}
	h.actors[id] = a
	h.mu.Unlock()
	// 抢写租约：纪元随每次抢租约递增，本 actor 的每次写入都带它（02 2.2 fencing）。
	holder := h.leaseHolder()
	epoch, err := h.DB.AcquireLease(ctx, id, holder)
	if err != nil {
		h.remove(id)
		return Handle{}, err
	}
	// 休眠中的会话：抢到租约即唤醒（02 2.2）。状态迁移也带新纪元，旧节点迟到的迁移被拒。
	st, err := h.DB.SessionStatus(ctx, id)
	if err == nil {
		switch st {
		case store.StatusSleeping:
			if err := h.DB.SetSessionStatus(ctx, id, store.StatusSleeping, store.StatusActive, epoch); err != nil {
				_ = h.DB.ReleaseLease(ctx, id, holder, epoch)
				h.remove(id)
				return Handle{}, err
			}
			h.publish(id, "session_wake", map[string]any{"epoch": epoch})
		case "deleted":
			_ = h.DB.ReleaseLease(ctx, id, holder, epoch)
			h.remove(id)
			return Handle{}, ErrClosed
		}
	}
	ag.Epoch = epoch
	a.epoch, a.holder = epoch, holder
	go a.run()
	return Handle{a}, nil
}

// leaseHolder 本实例的租约持有者标识（懒生成）。
func (h *Host) leaseHolder() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.Holder == "" {
		h.Holder = "host-" + uuid.NewString()
	}
	return h.Holder
}

// created 调 create 钩子；失败回滚（删除刚建的会话，DSH agent-loop 同做法）。
func (h *Host) created(ctx context.Context, id string, o Origin) error {
	if h.Hooks.OnCreate == nil {
		return nil
	}
	if err := h.Hooks.OnCreate(id, o); err != nil {
		_ = h.DB.DeleteSession(ctx, id, nil)
		return fmt.Errorf("create 钩子拒绝（已回滚）: %w", err)
	}
	return nil
}

func (h *Host) queue() int {
	if h.Queue > 0 {
		return h.Queue
	}
	return 64
}

func (h *Host) remove(id string) {
	h.mu.Lock()
	delete(h.actors, id)
	h.mu.Unlock()
}

func (h *Host) publish(id, kind string, data any) {
	if h.Bus != nil {
		h.Bus.Publish(bus.Envelope{Session: id, Kind: kind, Data: data})
	}
}

// NewItemID 生成 inbox item id（调用方没有时用）。
func NewItemID() string { u, _ := uuid.NewV7(); return u.String() }

// —— actor ——//

type actor struct {
	h    *Host
	id   string
	ag   *agent.Agent
	in   chan Command
	exit chan struct{}

	epoch  int64  // 写租约纪元（02 2.2）
	holder string // 租约持有者

	mu      sync.Mutex
	queued  []Command // 回合中收到、待需求边界 / 回合后处理的命令
	cancel  context.CancelFunc
	inTurn  bool
	waiters map[string]chan bool // 等确认的调用
	// processing 是 drainInbox 当前回合正在消费的 inbox 项（T2a-1：回合中插入不重复取同一条）。
	processing map[string]bool
	exitErr    error
	once       sync.Once
}

// enqueue 外部命令入口：Cancel / Confirm 立即生效；其余排队。
func (a *actor) enqueue(c Command) error {
	select {
	case <-a.exit:
		return ErrClosed
	default:
	}
	switch c.Kind {
	case Cancel:
		a.mu.Lock()
		if a.cancel != nil {
			a.cancel()
		}
		a.mu.Unlock()
		reply(c, nil)
		return nil
	case Confirm:
		a.mu.Lock()
		w, ok := a.waiters[c.CallID]
		a.mu.Unlock()
		if !ok {
			reply(c, fmt.Errorf("host: 调用 %s 不在等待确认", c.CallID))
			return nil
		}
		select {
		case w <- c.Approve:
		default:
		}
		reply(c, nil)
		return nil
	case Send, Note:
		if c.ItemID == "" {
			c.ItemID = NewItemID()
		}
		kind := store.InboxInput
		if c.Kind == Note {
			kind = store.InboxNote
		}
		body, _ := json.Marshal(session.Content{session.NewTextPart(c.Text)})
		fresh, err := a.h.DB.Admit(context.Background(), a.id, store.InboxItem{ItemID: c.ItemID, Kind: kind,
			Input: string(body), Trust: c.Trust})
		if err != nil {
			return err
		}
		reply(c, nil) // 先落 inbox 再确认（D2）
		if !fresh {
			return nil // 重放：首次准入胜出，不再触发（D6）
		}
		c.done = nil
		select {
		case a.in <- c: // 唤醒 actor 处理 inbox
		default: // 队列满说明 actor 还有待办命令，处理完会顺带消费 inbox
		}
		return nil
	}
	select {
	case a.in <- c:
		return nil
	default:
		return ErrBusy
	}
}

func reply(c Command, err error) {
	if c.done != nil {
		c.done <- err
	}
}

func (a *actor) run() {
	defer func() {
		if r := recover(); r != nil { // panic：不写头部，下次从最后一次 checkpoint 恢复
			a.exitErr = fmt.Errorf("host: 会话 %s panic: %v", a.id, r)
		}
		a.h.remove(a.id)
		if a.holder != "" { // 释放写租约；不匹配（已被接管）时存储忽略
			_ = a.h.DB.ReleaseLease(context.Background(), a.id, a.holder, a.epoch)
		}
		a.once.Do(func() { close(a.exit) })
	}()
	ctx := context.Background()
	// 打开时有未完成回合：先续跑（WaitingModel 原样重发，WaitingTools 按原 id 续答）。
	if a.ag.Pending() {
		end, err := a.turn(ctx, func(tctx context.Context) (session.EndReason, error) { return a.ag.Recover(tctx) })
		if err == nil {
			err = turnErr(end)
		}
		if err != nil {
			if a.turnFailed(ctx, err) {
				return
			}
		}
	}
	if err := a.drainInbox(ctx); err != nil {
		a.exitErr = err
		return
	}
	for {
		var sleepC <-chan time.Time
		var timer *time.Timer
		if a.h.SleepAfter > 0 {
			timer = time.NewTimer(a.h.SleepAfter)
			sleepC = timer.C
		}
		var c Command
		select {
		case c = <-a.in:
			if timer != nil {
				timer.Stop()
			}
		case <-sleepC:
			slept, err := a.sleep(ctx)
			if err != nil {
				a.exitErr = err
				return
			}
			if slept {
				return
			}
			continue
		}
		stop, err := a.idle(ctx, c)
		if c.Kind != "barrier" {
			reply(c, err)
		}
		if stop {
			return
		}
		if err := a.drainInbox(ctx); err != nil {
			reply(c, err)
			a.exitErr = err
			return
		}
		if c.Kind == "barrier" { // Wait：inbox 处理完才返回
			reply(c, nil)
		}
	}
}

// sleep 空闲超时：把会话置为休眠并让 actor 退出（defer 释放租约与对象，02 2.2）。
// 还有未完成回合、未处理信件、预算挂起或刚到的命令时不休眠。返回 true 表示已休眠。
func (a *actor) sleep(ctx context.Context) (bool, error) {
	if a.ag.Pending() {
		return false, nil
	}
	// 预算挂起的会话保持活跃（对象与租约都不释放），只是不开回合。
	if st, err := a.h.DB.SessionStatus(ctx, a.id); err == nil && st != store.StatusActive {
		return false, nil
	}
	items, err := a.h.DB.Inbox(ctx, a.id)
	if err != nil {
		return false, err
	}
	if len(items) > 0 {
		return false, nil
	}
	// 队列里可能有刚到的 Idle 命令：放回去继续处理，不睡。
	select {
	case c := <-a.in:
		select {
		case a.in <- c:
		default:
		}
		return false, nil
	default:
	}
	a.mu.Lock()
	busy := a.inTurn
	a.mu.Unlock()
	if busy {
		return false, nil
	}
	// 条件迁移带自己的纪元：状态被别人推进（如已唤醒）或纪元不符时留在 active 继续等。
	if err := a.h.DB.SetSessionStatus(ctx, a.id, store.StatusActive, store.StatusSleeping, a.epoch); err != nil {
		if errors.Is(err, store.ErrStateMismatch) {
			return false, nil
		}
		return false, err
	}
	a.h.publish(a.id, "session_sleep", map[string]any{"epoch": a.epoch})
	return true, nil
}

// ScanOnce 兜底扫描：唤醒信箱里有信件的休眠会话（02 2.2）。并发扫描由租约决定谁生效。
func (h *Host) ScanOnce(ctx context.Context) (int, error) {
	ids, err := h.DB.SessionsWithStatus(ctx, store.StatusSleeping, 64)
	if err != nil {
		return 0, err
	}
	woke := 0
	for _, id := range ids {
		items, err := h.DB.Inbox(ctx, id)
		if err != nil || len(items) == 0 {
			continue // 已关闭 / 被删 / 没有信件：跳过
		}
		if _, err := h.Open(ctx, id); err != nil {
			continue // 已被别的节点接管或已关闭：跳过
		}
		woke++
	}
	return woke, nil
}

// ScanLoop 周期跑 ScanOnce，直到 ctx 取消。
func (h *Host) ScanLoop(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = 5 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_, _ = h.ScanOnce(ctx)
		}
	}
}

// Suspend 预算挂起（02 2.2）：停在检查点、不再开新回合，信箱里的信件保留；
// 补足预算或应用显式 Resume 后从检查点继续（T3 起由守卫在预算用尽时调用）。
func (h *Host) Suspend(ctx context.Context, id, reason string) error {
	h.mu.Lock()
	a := h.actors[id]
	h.mu.Unlock()
	if a == nil {
		return fmt.Errorf("host: 会话 %s 没打开，不能挂起", id)
	}
	a.mu.Lock()
	busy := a.inTurn
	a.mu.Unlock()
	if busy {
		return fmt.Errorf("host: 会话 %s 正在运行，不能在回合中途挂起", id)
	}
	if err := h.DB.SetSessionStatus(ctx, id, store.StatusActive, store.StatusSuspended, a.epoch); err != nil {
		return err
	}
	if err := a.ag.Core.SetExt("rt.suspended", reason); err != nil {
		return err
	}
	if err := a.ag.CommitRun(ctx, "suspended"); err != nil {
		return err
	}
	h.publish(id, "session.suspended", map[string]any{"reason": reason})
	return nil
}

// cancelChildren 取消本会话直接派生、仍在运行的子会话（Rewind 的所属树处理，02 2.2）。
// 只处理内存里打开的子会话：没打开的子会话没有进行中的一轮，无需取消。
func (h *Host) cancelChildren(ctx context.Context, parent string) {
	list, err := h.DB.ListSessions(ctx, 0, 500, false)
	if err != nil {
		return
	}
	for _, m := range list {
		if m.ParentID != parent {
			continue
		}
		hd, ok := h.Get(m.ID)
		if !ok {
			continue
		}
		_ = hd.Do(Command{Kind: Close})
		h.publish(m.ID, "session.closed", map[string]any{"reason": "parent_rewind"})
	}
}

// idle 主循环只处理 Idle（H6）。返回 true 表示 actor 退出。
func (a *actor) idle(ctx context.Context, c Command) (bool, error) {
	core := a.ag.Core
	switch c.Kind {
	case Send, Note, "barrier":
		return false, nil // 已落 inbox，由 drainInbox 处理
	case Resume:
		// 预算挂起 → 恢复（02 2.2）：不重开回合，回到活跃并交给主循环处理信箱。
		if st, serr := a.h.DB.SessionStatus(ctx, a.id); serr == nil && st == store.StatusSuspended {
			if err := a.h.DB.SetSessionStatus(ctx, a.id, store.StatusSuspended, store.StatusActive, a.epoch); err != nil {
				return false, err
			}
			_ = core.SetExt("rt.suspended", nil)
			if err := a.ag.CommitRun(ctx, "idle"); err != nil {
				return false, err
			}
			a.h.publish(a.id, "session.resumed", nil)
			return false, nil
		}
		_, need, err := core.Resume()
		if err != nil {
			return false, err
		}
		end, terr := a.turn(ctx, func(tctx context.Context) (session.EndReason, error) { return a.ag.Drive(tctx, need) })
		if terr == nil {
			terr = turnErr(end)
		}
		if terr != nil && a.turnFailed(ctx, terr) {
			return true, terr
		}
		return false, nil
	case Steer:
		return false, session.ErrNotWaiting
	case Record:
		if _, err := core.Record(text(c.Text), meta(c)); err != nil {
			return false, err
		}
		return false, a.ag.Commit(ctx)
	case SetExt:
		if err := core.SetExt(c.Key, c.Value); err != nil {
			return false, err
		}
		return false, a.ag.Commit(ctx)
	case SetConfig:
		if _, _, err := core.SetConfig(session.ConfigPatch{MaxSteps: c.MaxSteps}); err != nil {
			return false, err
		}
		return false, a.ag.Commit(ctx)
	case Compact:
		if _, err := core.Compact(text(c.Text), c.Upto, map[string]any{"source": "user"}); err != nil {
			return false, err
		}
		return false, a.ag.Commit(ctx)
	case Rewind:
		if _, err := core.Rewind(c.To); err != nil {
			return false, err
		}
		if err := a.ag.Commit(ctx); err != nil {
			return false, err
		}
		a.h.publish(a.id, "session.rewound", map[string]any{"to": c.To})
		// 所属树（02 2.2）：回退点之后派生出的子会话取消；作废其 corr 随 T3 信封一起做。
		a.h.cancelChildren(ctx, a.id)
		return false, nil
	case Drain:
		_ = core.SetExt("rt.drained", true) // 区分正常退出与崩溃（D1）
		return true, a.ag.Commit(ctx)
	case Close:
		return true, a.ag.Commit(ctx)
	}
	return false, fmt.Errorf("host: 未知命令 %q", c.Kind)
}

// drainInbox 处理 inbox：note 拼在 input 前，逐个 input 开回合（H7 / H10）。只有 note 时留到下一个输入。
func (a *actor) drainInbox(ctx context.Context) error {
	// 预算挂起：信箱保留、不开新回合（02 2.2：补足预算或显式 Resume 后从检查点继续）。
	if st, err := a.h.DB.SessionStatus(ctx, a.id); err == nil && st == store.StatusSuspended {
		return nil
	}
	for {
		items, err := a.h.DB.Inbox(ctx, a.id)
		if err != nil {
			return err
		}
		var notes []store.InboxItem
		var input *store.InboxItem
		for i := range items {
			if items[i].Kind == store.InboxNote {
				notes = append(notes, items[i])
				continue
			}
			input = &items[i]
			break
		}
		if input == nil {
			return nil
		}
		var content session.Content
		untrusted := false
		consume := []string{}
		for _, it := range append(notes, *input) {
			var c session.Content
			if err := json.Unmarshal([]byte(it.Input), &c); err != nil {
				return err
			}
			if it.Kind == store.InboxNote {
				c = append(session.Content{session.NewTextPart("[补报] ")}, c...)
			}
			content = append(content, c...)
			if it.Trust == "untrusted" {
				untrusted = true // 取最低（H10：防不可信补报被洗白）
			}
			consume = append(consume, it.ItemID)
		}
		a.processing = inboxSet(consume) // 本条正在被本回合消费：回合中插入不得重复取它
		m := map[string]any{session.MetaSource: "user", session.MetaTrust: "trusted"}
		if untrusted {
			m[session.MetaTrust] = "untrusted"
		}
		if hk := a.h.Hooks.BeforeTurn; hk != nil {
			nc, d, err := hk(a.id, content, untrusted)
			if err != nil {
				d = Deny // Guard FailClosed
			}
			switch d {
			case Deny:
				a.h.publish(a.id, "turn_denied", map[string]any{"unconsumed": content})
				if err := a.ag.Commit(ctx, consume...); err != nil {
					return err
				}
				a.processing = nil
				continue
			case RecordD:
				if _, err := a.ag.Core.Record(content, m); err != nil {
					return err
				}
				if err := a.ag.Commit(ctx, consume...); err != nil {
					return err
				}
				a.processing = nil
				continue
			}
			content = nc
		}
		end, terr := a.turn(ctx, func(tctx context.Context) (session.EndReason, error) {
			return a.ag.SendContent(tctx, content, m, consume)
		})
		if terr == nil {
			terr = turnErr(end)
		}
		if terr != nil {
			if a.turnFailed(ctx, terr) {
				return terr
			}
		}
		a.processing = nil
	}
}

// inboxSet 把 inbox 项 id 集合化（回合中插入时跳过本回合正在消费的项）。
func inboxSet(ids []string) map[string]bool {
	if len(ids) == 0 {
		return nil
	}
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

// turn 跑一个回合：可取消的 ctx、turn span、after_turn、回合中排队的 Idle 命令在回合后处理。
func (a *actor) turn(ctx context.Context, f func(context.Context) (session.EndReason, error)) (session.EndReason, error) {
	release, err := a.acquireSlot(ctx)
	if err != nil {
		return session.EndReason{}, err
	}
	defer release()
	tctx, cancel := context.WithCancel(ctx)
	a.mu.Lock()
	a.cancel, a.inTurn = cancel, true
	a.mu.Unlock()
	defer func() {
		cancel()
		a.mu.Lock()
		a.cancel, a.inTurn = nil, false
		a.mu.Unlock()
	}()
	sp := a.ag.StartSpan("turn", "", "")
	a.ag.TurnSpanID = sp.ID()
	defer func() { a.ag.TurnSpanID = "" }()
	a.h.publish(a.id, "turn_start", nil)
	end, err := f(tctx)
	if err != nil {
		sp.Set("error", err.Error()).End("error")
		return end, err
	}
	sp.Set("end", end.String()).End(end.String())
	if end.Kind == session.EndCancelled {
		a.h.publish(a.id, "turn.interrupted", nil)
	}
	if a.h.Hooks.AfterTurn != nil {
		a.h.Hooks.AfterTurn(a.id, end)
	}
	a.h.publish(a.id, "turn_end", map[string]any{"reason": end.String()})
	return end, a.ag.Commit(ctx) // span 与 after_turn 的修改落库
}

// turnErr 把「回合以失败收尾」（drive 正常返回、EndReason 是 Failed）转成错误，供所属树策略处理。
func turnErr(end session.EndReason) error {
	if end.Kind != session.EndFailed {
		return nil
	}
	msg := end.Message
	if msg == "" {
		msg = end.String()
	}
	return fmt.Errorf("回合失败：%s", msg)
}

// turnFailed 处理一轮运行的失败（02 2.2 的所属树策略）。返回 true 表示 actor 应退出，
// 会话句柄的 Wait 会得到这个错误；false 表示已按策略重跑/降级，actor 继续服务。
func (a *actor) turnFailed(ctx context.Context, err error) bool {
	a.h.publish(a.id, "turn.failed", map[string]any{"error": err.Error()})
	// 失败的一轮不留在等待态：先收尾成 Idle（未完成的工具按 Abort 语义处理）。
	if a.ag.Core.Status() != session.StatusIdle {
		_, _, _ = a.ag.Core.Abort(session.FailedReason("TURN_FAILED"))
	}
	if cerr := a.ag.CommitRun(ctx, "idle"); cerr != nil {
		a.exitErr = cerr
		return true
	}
	if a.ag.Origin == "" { // 顶层会话：保持原行为，错误原样交回调用方
		a.exitErr = err
		return true
	}
	switch a.h.treePolicy() {
	case "restart":
		max := a.h.TreeRestarts
		if max <= 0 {
			max = 1
		}
		for i := 0; i < max; i++ {
			end, rerr := a.turn(ctx, a.ag.Retry)
			if rerr == nil {
				rerr = turnErr(end)
			}
			if rerr == nil {
				return false
			}
			err = rerr
		}
		a.exitErr = err
		return true
	case "escalate":
		a.h.publish(a.id, "session.escalated", map[string]any{"error": err.Error()})
		a.exitErr = err
		return true
	default: // give_up：标记失败并告知父会话（同步子智能体的错误随句柄返回）
		a.exitErr = err
		return true
	}
}

// treePolicy 所属树策略，缺省 give_up。
func (h *Host) treePolicy() string {
	switch h.TreePolicy {
	case "restart", "escalate":
		return h.TreePolicy
	default:
		return "give_up"
	}
}

// between 需求边界（回合内）：取出回合中到达的命令，插话 / 改向 / Idle 类配置在这里交给内核。
func (a *actor) between(need *session.Need) (*session.Need, error) {
	for {
		var c Command
		select {
		case c = <-a.in:
		default:
			return need, nil
		}
		switch c.Kind {
		case Steer:
			_, n, err := a.ag.Core.Steer(text(c.Text), meta(c), steerMode(c.Mode))
			reply(c, err)
			if err == nil && n != nil {
				need = n // WaitingTools 改向：未执行调用补 Redirected，直接进入下一步
			}
		case Send, Note:
			// 运行中到达的用户消息：在下一次调模型前插入当前回合（09 9.4）。
			// 已到 Done 边界时留给下一回合，inbox 不动。
			if need.Kind != session.NeedDone {
				reply(c, a.insertInput(context.Background()))
			}
		case SetConfig: // 内核暂存到下一个步边界，随下次 checkpoint 落库
			_, _, err := a.ag.Core.SetConfig(session.ConfigPatch{MaxSteps: c.MaxSteps})
			reply(c, err)
		case SetExt: // 只改头部 Ext，随下次 checkpoint 落库；不必等回合结束
			reply(c, a.ag.Core.SetExt(c.Key, c.Value))
		default:
			// Idle 类命令：回合结束后由主循环处理（放回队尾会打乱顺序，所以存到 queued）
			a.mu.Lock()
			a.queued = append(a.queued, c)
			a.mu.Unlock()
			go a.requeue()
		}
	}
}

// requeue 回合结束后把暂存的 Idle 命令放回队列。
func (a *actor) requeue() {
	for {
		a.mu.Lock()
		busy := a.inTurn
		q := a.queued
		if !busy {
			a.queued = nil
		}
		a.mu.Unlock()
		if !busy {
			for _, c := range q {
				select {
				case a.in <- c:
				case <-a.exit:
					return
				}
			}
			return
		}
		select {
		case <-a.exit:
			return
		case <-timeAfter():
		}
	}
}

// wireConfirm 把 toolhub 的人工确认接到 Confirm 命令（单一路径，D3）。
func (a *actor) wireConfirm() {
	a.ag.Hub.SetAwait(a.ag.AwaitFunc(func(cx *toolhub.Cx, reason string) bool {
		w := make(chan bool, 1)
		a.mu.Lock()
		a.waiters[cx.CallID] = w
		a.mu.Unlock()
		defer func() {
			a.mu.Lock()
			delete(a.waiters, cx.CallID)
			a.mu.Unlock()
		}()
		a.h.publish(a.id, "human_requested", map[string]any{"call_id": cx.CallID, "tool": cx.Tool.Spec.Name, "reason": reason, "args": string(cx.Args)})
		select {
		case ok := <-w:
			return ok
		case <-a.exit:
			return false
		}
	}))
}

func text(s string) session.Content { return session.Content{session.NewTextPart(s)} }

func meta(c Command) map[string]any {
	src := c.Source
	if src == "" {
		src = "user"
	}
	m := map[string]any{session.MetaSource: src}
	if c.Trust != "" {
		m[session.MetaTrust] = c.Trust
	}
	return m
}

func steerMode(m session.SteerMode) session.SteerMode {
	if m == "" {
		return session.SteerAppend
	}
	return m
}

// timeAfter requeue 的轮询间隔。
func timeAfter() <-chan time.Time { return time.After(20 * time.Millisecond) }

// acquireSlot 取一个并发推理许可（Host.Limit 控制）；超出上限时排队等待，不报错（02 2.4）。
func (a *actor) acquireSlot(ctx context.Context) (func(), error) {
	h := a.h
	if h.Limit <= 0 {
		return func() {}, nil
	}
	h.mu.Lock()
	if h.sem == nil || cap(h.sem) != h.Limit {
		h.sem = make(chan struct{}, h.Limit)
	}
	sem := h.sem
	h.mu.Unlock()
	select {
	case sem <- struct{}{}:
		return func() { <-sem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-a.exit:
		return nil, ErrClosed
	}
}

// insertInput 把 inbox 里待处理的用户消息插入当前回合：note 拼在 input 前，下一次调模型前生效
// （session 的暂存插话机制）；只有 note 时留到下一个输入（与 drainInbox 一致）。
// 守卫拒绝或内核不需要插话时保持 inbox 不动，交给回合结束后的正常路径处理。
func (a *actor) insertInput(ctx context.Context) error {
	items, err := a.h.DB.Inbox(ctx, a.id)
	if err != nil {
		return err
	}
	var notes []store.InboxItem
	var input *store.InboxItem
	for i := range items {
		if a.processing[items[i].ItemID] {
			continue // 本回合正在消费的项（drainInbox 已取），不重复插入
		}
		if items[i].Kind == store.InboxNote {
			notes = append(notes, items[i])
			continue
		}
		input = &items[i]
		break
	}
	if input == nil {
		return nil
	}
	var content session.Content
	untrusted := false
	var ids []string
	for _, it := range append(notes, *input) {
		var c session.Content
		if err := json.Unmarshal([]byte(it.Input), &c); err != nil {
			return err
		}
		if it.Kind == store.InboxNote {
			c = append(session.Content{session.NewTextPart("[补报] ")}, c...)
		}
		content = append(content, c...)
		if it.Trust == "untrusted" {
			untrusted = true
		}
		ids = append(ids, it.ItemID)
	}
	// 守卫仍要过：拒绝时不插入，留给回合后的正常处理（那里会发布 turn_denied 并消费）。
	if hk := a.h.Hooks.BeforeTurn; hk != nil {
		nc, d, err := hk(a.id, content, untrusted)
		if err != nil || d != Allow {
			return nil
		}
		content = nc
	}
	m := map[string]any{session.MetaSource: "user", session.MetaTrust: "trusted"}
	if untrusted {
		m[session.MetaTrust] = "untrusted"
	}
	if _, err := a.ag.Core.MidInput(content, m); err != nil {
		if errors.Is(err, session.ErrNotWaiting) {
			return nil // 回合刚好结束：inbox 留给下一回合
		}
		return err
	}
	a.ag.Consume(ids...)
	return nil
}
