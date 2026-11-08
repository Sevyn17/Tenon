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
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/Sevyn17/tenon/runtime/agent"
	"github.com/Sevyn17/tenon/runtime/bus"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// 错误。
var (
	ErrBusy   = errors.New("host: 命令队列已满")
	ErrClosed = errors.New("host: 会话已关闭")
	// ErrStale 命令带的 IfCursor 与会话当前游标不符（protocol 2 乐观并发，06 10.4）。
	ErrStale = errors.New("host: 会话游标已变化")
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
	// SetConfig 改会话配置（外部白名单：MaxSteps、Model 与推理强度 Reasoning，其余 Params 不开放）；Idle 立即生效，回合中到步边界生效。
	SetConfig Kind = "set_config"
	Compact   Kind = "compact"
	Rewind    Kind = "rewind"
	Close     Kind = "close"
	Drain     Kind = "drain"
	// Deliver 信封已落 inbox，叫醒 actor 去 drainInbox（投递本身走 Host.Deliver）。
	Deliver Kind = "deliver"
	// Suspend 预算挂起（02 2.2）：Idle 命令；回合进行中到达时排到本回合结束后执行。Text = 原因。
	Suspend Kind = "suspend"
)

// Command 一条命令。
type Command struct {
	Kind     Kind
	ItemID   string // Send / Note：调用方生成，幂等键
	Text     string
	Content  session.Content // Send / Note：非空时代替 Text（图片、文件等部件，protocol 2）
	Trust    string          // trusted / untrusted
	Source   string          // user / agent / system …
	Mode     session.SteerMode
	CallID   string  // Confirm
	Approve  bool    // Confirm
	To       int     // Rewind
	Upto     int     // Compact
	Key      string  // SetExt
	Value    any     // SetExt
	MaxSteps *uint32 // SetConfig：单回合步数上限，0 = 不限
	Model    *string // SetConfig：模型别名（调用方先按目录校验）
	// Reasoning SetConfig：推理强度（low / medium / high，"" = 厂商缺省；调用方先校验取值）。
	Reasoning *string
	// IfCursor Rewind / Compact：非空时执行前核对会话事件游标，不符返回 ErrStale（多端同时操作时防止拿着过期界面改日志）。
	IfCursor *int64
	// ToTurn Rewind：非零时按回合号截断（保留 ToTurn 之前的回合），在 actor 里解析成下标，代替 To（protocol 2 rewind {to_turn}）。
	ToTurn uint32
	// RewoundTo Rewind：非空时写回实际截到的下标（目标回合已不存在时为日志长度，什么都不截）。
	RewoundTo *int
	// Committed SetExt / SetConfig：非空时，空闲下已提交则置 true；回合中只改内存、随下个检查点落库时保持 false（protocol 2 committed）。
	Committed *bool
	done      chan error
}

// Decision before_turn 的结论。
type Decision int

const (
	Allow      Decision = iota
	Deny                // 不开回合，输入交还
	RecordOnly          // 只记录，不开回合（转人工）
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
	store.OutboxStore
	store.CorrStore
	store.InteractionStore
	store.TraceStore
}

// Host 进程内会话表。
type Host struct {
	DB    Store
	New   Factory
	Child Factory // 子智能体会话的构造（可空 = New）；通常去掉委派工具以限制深度
	Hooks Hooks
	Bus   *bus.Bus // 可空：易失事件（界面）
	Queue int      // 外部队列容量，默认 64
	// Workspace 本宿主所在的工作区键（应用给，如规范化后的项目目录）：新建的顶层会话记上它（子会话由存储跟随父会话）；
	// 非空时拒绝打开属于别的工作区的会话——会话的工具在本宿主的工作目录里执行，开错了就是在别的项目里读写。
	// 空 = 不区分工作区。
	Workspace string

	// Holder 本实例的租约持有者标识（空 = 首次打开时按实例生成）。
	// 同一存储上两个 Host 打开同一会话会互相抢租约，旧持有者的写入被存储拒绝（02 2.2 fencing）。
	Holder string
	// Limit 同时推理的会话数上限（0 = 不限）；超出上限的回合排队等待，不报错（02 2.4）。
	// 装配时设置；第一个回合开始后再改不生效。
	Limit int

	// 人工确认（11 11.2 human: 地址）：内核签发 corr、发布 human.asked；
	// 回信（KindReply + corr）投回会话时按 corr 交给等待中的工具确认。
	humanMu   sync.Mutex
	humanWait map[string]chan bool
	// askWait 非工具交互（question、choice、form……）的等待方：corr → 回答通道（Host.Interact / Host.Respond，
	// protocol 2 10.6）。同受 humanMu 保护。
	askWait map[string]chan Answer
	// SleepAfter 空闲多久后休眠（0 = 不休眠）。休眠的会话释放进程内对象与写租约，
	// 只剩存储里的几行；有新信件时任一节点抢到租约即可唤醒（02 2.2）。
	SleepAfter time.Duration
	// TreePolicy 所属树策略（02 2.2）：子会话一轮运行失败时 restart | give_up | escalate；
	// 空/未知按 give_up（标记失败并告知父会话——当前实现是 actor 退出、错误原样交回调用方）。
	TreePolicy string
	// TreeRestarts restart 策略下的最大重跑次数（<=0 按 1）。
	TreeRestarts int
	// Guards 组件登记的守卫规则（04 4.2、08 D4），装配期从 Assembly 取来。
	Guards []component.GuardRule

	mu     sync.Mutex
	actors map[string]*actor
	sem    chan struct{} // Limit 的许可池，首次使用时按 Limit 建

	// 发件箱派发（08 9.6）：同一时刻只有一个派发者（读未发行 → 投递 → 标记之间不能交错，
	// 否则同一条会投递多次）；回合结束只「踢一下」后台派发，不在 actor 协程里投递
	// （发给自己 / 父会话的 control 会同步等 actor 受理命令）。
	flushRun   sync.Mutex
	flushMu    sync.Mutex
	flushing   bool
	flushAgain bool
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

// Post 投递命令，入队即返回、不等受理（队列满返回 ErrBusy，已退出返回 ErrClosed）。
// 发件箱派发 control 用：Close 在回合中要等回合结束才执行，同步等会卡住整个派发、超过认领期限后被重复投递
// （2026-11-04 审查 P0-6）。
func (h Handle) Post(c Command) error {
	c.done = nil
	return h.a.enqueue(c)
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

// InTurn 会话是否有进行中的回合（protocol 2：send 的 queued、需空闲命令的 not_idle 判断）。
func (h Handle) InTurn() bool {
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	return h.a.inTurn
}

// Open 打开（不存在则新建，origin = new）。已打开的返回同一句柄。
// 已存在的子会话（origin = subagent / member）按 Child 构造：休眠唤醒、重启后收信、control
// 都走这里，用 New 会让委派工具重新可见、深度限制失效（2026-11-04 审查 P0-5）。
func (h *Host) Open(ctx context.Context, id string) (Handle, error) {
	if hd, ok := h.Get(id); ok {
		return hd, nil
	}
	f := h.New
	if h.Child != nil {
		child, err := h.isChild(ctx, id)
		if err != nil {
			return Handle{}, err
		}
		if child {
			f = h.Child
		}
	}
	return h.open(ctx, id, "", f, nil)
}

// OpenExisting 同 Open，但会话不存在时返回 ErrSessionNotFound，不新建（protocol 2 G1：任何命令都不隐式新建会话）。
func (h *Host) OpenExisting(ctx context.Context, id string) (Handle, error) {
	if hd, ok := h.Get(id); ok {
		return hd, nil
	}
	ok, err := h.DB.Exists(ctx, id)
	if err != nil {
		return Handle{}, err
	}
	if !ok {
		return Handle{}, fmt.Errorf("%w: %s", ErrSessionNotFound, id)
	}
	return h.Open(ctx, id)
}

// Create 新建顶层会话（origin = new）并打开；def 为智能体定义名（空 = 无定义会话）。
// bizMeta 写进会话行的业务键（可空）。会话已存在返回 store.ErrConflict（protocol 2 session.create）。
func (h *Host) Create(ctx context.Context, id, def string, bizMeta json.RawMessage) (Handle, error) {
	if ok, err := h.DB.Exists(ctx, id); err != nil {
		return Handle{}, err
	} else if ok {
		return Handle{}, fmt.Errorf("%w: 会话 %s 已存在", store.ErrConflict, id)
	}
	hd, err := h.open(ctx, id, def, h.New, func(ag *agent.Agent, cfg *session.Config) error {
		meta := store.SessionMeta{Origin: "new", Meta: bizMeta, Workspace: h.Workspace}
		if ag.Def != nil {
			meta.AgentID, meta.AgentHash = ag.Def.ID, ag.Def.Version
		}
		_, err := ag.OpenAs(ctx, cfg, meta)
		return err
	})
	if err != nil {
		return Handle{}, err
	}
	if err := h.created(ctx, id, Origin{Kind: "new"}); err != nil {
		_ = hd.Do(Command{Kind: Close})
		return Handle{}, err
	}
	return hd, nil
}

// head 只读会话行（元数据与头部），不读也不反序列化任何条目：base 取最大值（审查 P1-34 / P1-35）。
func (h *Host) head(ctx context.Context, id string) (*store.Loaded, error) {
	return LoadHead(ctx, h.DB, id)
}

// LoadHead 只读会话行与头部、不读条目的 Load（base 取最大值，两种存储都返回空条目）。
// 投递、翻页这类只要元数据的路径用它，成本不随日志长度增长。
func LoadHead(ctx context.Context, db interface {
	Load(ctx context.Context, id string, base *int) (*store.Loaded, error)
}, id string) (*store.Loaded, error) {
	noEntries := math.MaxInt
	return db.Load(ctx, id, &noEntries)
}

// isChild 会话是否是子会话（子智能体或工作组成员）；会话不存在返回 false。
// 只要会话行：base 取到最大，不读任何条目。
func (h *Host) isChild(ctx context.Context, id string) (bool, error) {
	l, err := h.head(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return l.Meta.ParentID != "" && (l.Meta.Origin == "subagent" || l.Meta.Origin == "member"), nil
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
	if err := h.checkWorkspace(ctx, id); err != nil {
		return Handle{}, err
	}

	ag, cfg, err := f(id, def)
	if err != nil {
		return Handle{}, err
	}
	a := &actor{h: h, id: id, ag: ag, in: make(chan Command, h.queue()), exit: make(chan struct{}),
		waiters: map[string]chan bool{}}
	a.mailDirty.Store(true) // 打开时可能有积压信件：第一次步边界读一次
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
	ag.InstallCapsGuard() // 信封 caps 交集限制本轮可调用的工具（T3）
	// 上次进程留下的未结束交互：等它的人已经不在了，标为 voided（恢复的工具调用会重新询问，06 10.6）。
	h.voidStale(ctx, id)
	go a.run()
	return Handle{a}, nil
}

// ErrOtherWorkspace 会话属于另一个工作区（Host.Workspace）。
var ErrOtherWorkspace = errors.New("host: 会话属于另一个工作区")

// checkWorkspace 已有会话属于别的工作区时拒绝打开；没记工作区的旧会话、新会话照常。
func (h *Host) checkWorkspace(ctx context.Context, id string) error {
	if h.Workspace == "" {
		return nil
	}
	l, err := h.head(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if w := l.Meta.Workspace; w != "" && w != h.Workspace {
		return fmt.Errorf("%w（%s）：切到那个工作区再打开", ErrOtherWorkspace, w)
	}
	return nil
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

	mu     sync.Mutex
	queued []Command // 回合中收到、待需求边界 / 回合后处理的命令（回合结束后由 flushQueued 同步执行）
	// barriers 回合中到达的 Wait：信箱处理完再回复。只在 actor 协程里读写。
	barriers []Command
	cancel   context.CancelFunc
	inTurn   bool
	waiters  map[string]chan bool // 等确认的调用
	// processing 是 drainInbox 当前回合正在消费的 inbox 项（T2a-1：回合中插入不重复取同一条）。
	processing map[string]bool
	// turnQueued 是回合开始时就已在信箱里的项：它们属于本回合或更早，不中途插入，留给下一回合（13 13.3 分轮）。
	turnQueued map[string]bool
	// turnCaps 是本轮的工具授权范围（信封 caps 交集）：turn 开始时设给 agent，结束时清空。
	turnCaps []string
	// steered 本回合已受理的插话 client_id（protocol 2 steer 幂等：插话只在回合中有效，同回合重发只确认不重复插入）。只在 actor 协程里读写。
	steered map[string]bool
	// turnInputs 即将开始的回合吃进的输入（inbox item id），turn_start 发出后清空。只在 actor 协程里读写。
	turnInputs []string
	// mailDirty 自上次在需求边界读信箱后，是否有新信件落进本会话信箱（审查 P1-36：原先每次调模型前都查一次信箱，
	// 绝大多数步没有新信）。经本宿主写信箱的路径（投递、Send / Note、交还插话）都会置位；打开时置位以读到积压。
	mailDirty atomic.Bool
	exitErr   error
	once      sync.Once
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
	case Deliver:
		// 信封已由 Host.Deliver 落 inbox；这里只叫醒 actor（队列满时留给 actor 自己 drainInbox）。
		a.mailDirty.Store(true)
		reply(c, nil)
		select {
		case a.in <- c:
		default:
		}
		return nil
	case Send, Note:
		if c.ItemID == "" {
			c.ItemID = NewItemID()
		}
		kind := store.InboxInput
		if c.Kind == Note {
			kind = store.InboxNote
		}
		in := c.Content
		if len(in) == 0 {
			in = session.Content{session.NewTextPart(c.Text)}
		}
		body, _ := json.Marshal(in)
		fresh, err := a.h.DB.Admit(context.Background(), a.id, store.InboxItem{ItemID: c.ItemID, Kind: kind,
			Input: string(body), Trust: c.Trust})
		if err != nil {
			return err
		}
		a.mailDirty.Store(true)
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
	if stop, err := a.drainInbox(ctx); err != nil || stop {
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
		if stop, err := a.drainInbox(ctx); err != nil || stop {
			if c.Kind == "barrier" {
				reply(c, err)
			}
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
	if wakeable(items) { // 只剩背景信件不妨碍休眠：它们留在信箱，下一封 wake 唤醒时一起处理
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
	if sc, ok := h.DB.(store.SleepScanner); ok { // 一次查出有可唤醒信件的休眠会话（审查 P1-37）
		ids, err := sc.SleepingWithMail(ctx, []string{store.InboxInput, store.InboxNotice}, ScanBatch)
		if err != nil {
			return 0, err
		}
		woke := 0
		for _, id := range ids {
			if _, err := h.Open(ctx, id); err == nil {
				woke++
			}
		}
		return woke, nil
	}
	ids, err := h.DB.SessionsWithStatus(ctx, store.StatusSleeping, ScanBatch)
	if err != nil {
		return 0, err
	}
	woke := 0
	for _, id := range ids {
		items, err := h.DB.Inbox(ctx, id)
		if err != nil || !wakeable(items) {
			continue // 已关闭 / 被删 / 没有要处理的信件（只有背景信件不唤醒）：跳过
		}
		if _, err := h.Open(ctx, id); err != nil {
			continue // 已被别的节点接管或已关闭：跳过
		}
		woke++
	}
	return woke, nil
}

// ScanBatch 兜底扫描每轮最多唤醒的会话数。
const ScanBatch = 64

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
//
// 挂起在会话自己的 actor 里执行（会话核心只由 actor 改）：回合进行中调用时等本回合结束后
// 再挂起，返回时已生效。ctx 只约束等待。
func (h *Host) Suspend(ctx context.Context, id, reason string) error {
	hd, ok := h.Get(id)
	if !ok {
		return fmt.Errorf("host: 会话 %s 没打开，不能挂起", id)
	}
	c := Command{Kind: Suspend, Text: reason, done: make(chan error, 1)}
	if err := hd.a.enqueue(c); err != nil {
		return err
	}
	select {
	case err := <-c.done:
		return err
	case <-hd.a.exit:
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

// suspend 在 actor 里执行挂起（见 Host.Suspend）。
func (a *actor) suspend(ctx context.Context, reason string) error {
	if err := a.h.DB.SetSessionStatus(ctx, a.id, store.StatusActive, store.StatusSuspended, a.epoch); err != nil {
		return err
	}
	if err := a.ag.Core.SetExt("rt.suspended", reason); err != nil {
		return err
	}
	if err := a.ag.CommitRun(ctx, "suspended"); err != nil {
		return err
	}
	a.h.publish(a.id, "session.suspended", map[string]any{"reason": reason})
	return nil
}

// cancelChildren 取消本会话直接派生、仍在运行的子会话（Rewind 的所属树处理，02 2.2）。
// 只处理内存里打开的子会话：没打开的子会话没有进行中的一轮，无需取消。
func (h *Host) cancelChildren(ctx context.Context, parent string) {
	// 直接遍历已打开的 actor（审查 P1-38：原先扫 ListSessions(…, 500)，最多 500 条，
	// 会话多时较老的子会话会漏掉）。父会话 id 从会话行读。
	h.mu.Lock()
	var open []*actor
	for _, a := range h.actors {
		if a.id != parent {
			open = append(open, a)
		}
	}
	h.mu.Unlock()
	var kids []*actor
	for _, a := range open { // 只读打开着的会话的会话行（不读条目），数量随打开的会话数而不是全库
		if l, err := LoadHead(ctx, h.DB, a.id); err == nil && l.Meta.ParentID == parent {
			kids = append(kids, a)
		}
	}
	for _, a := range kids {
		_ = Handle{a}.Do(Command{Kind: Cancel}) // 进行中的回合先取消
		// 入队即返回：不在父会话的 actor 里等子回合收尾；session.closed 由关闭的检查点写成持久事件。
		_ = Handle{a}.Post(Command{Kind: Close, Text: "parent_rewind"})
	}
}

// idle 主循环只处理 Idle（H6）。返回 true 表示 actor 退出。
func (a *actor) idle(ctx context.Context, c Command) (bool, error) {
	core := a.ag.Core
	switch c.Kind {
	case Send, Note, Deliver, "barrier":
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
		need, err := core.Resume()
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
		if err := core.Record(text(c.Text), meta(c)); err != nil {
			return false, err
		}
		return false, a.ag.Commit(ctx)
	case SetExt:
		if err := core.SetExt(c.Key, c.Value); err != nil {
			return false, err
		}
		return false, markCommitted(c, a.ag.Commit(ctx))
	case SetConfig:
		if _, err := core.SetConfig(session.ConfigPatch{MaxSteps: c.MaxSteps, Model: c.Model, Reasoning: c.Reasoning}); err != nil {
			return false, err
		}
		return false, markCommitted(c, a.ag.Commit(ctx))
	case Compact:
		if err := a.checkCursor(ctx, c.IfCursor); err != nil {
			return false, err
		}
		if err := core.Compact(text(c.Text), c.Upto, map[string]any{"source": "user"}); err != nil {
			return false, err
		}
		return false, a.ag.Commit(ctx)
	case Rewind:
		if err := a.checkCursor(ctx, c.IfCursor); err != nil {
			return false, err
		}
		if c.ToTurn > 0 { // protocol 2 rewind {to_turn}：在 actor 里按已提交日志解析，和执行是同一个状态
			to, err := turnStart(core, c.ToTurn)
			if err != nil {
				return false, err
			}
			c.To = to
		}
		if c.RewoundTo != nil {
			*c.RewoundTo = c.To
		}
		if c.To == core.Snapshot().Header.LogLen { // 目标回合已不存在：什么都不截（重发同一 to_turn 无害）
			return false, nil
		}
		if err := core.Rewind(c.To); err != nil {
			return false, err
		}
		if err := a.ag.Commit(ctx); err != nil {
			return false, err
		}
		// Rewind 作废回退点之后签发的 corr（11 11.2、09 9.6）。
		voided, _ := a.h.DB.VoidCorrAfter(ctx, a.id, c.To)
		a.h.publish(a.id, "session.rewound", map[string]any{"to": c.To, "corr_voided": voided})
		// 所属树（02 2.2）：回退点之后派生出的子会话取消。
		a.h.cancelChildren(ctx, a.id)
		return false, nil
	case Suspend:
		return false, a.suspend(ctx, c.Text)
	case Drain:
		_ = core.SetExt("rt.drained", true) // 区分正常退出与崩溃（D1）
		return true, a.ag.Commit(ctx)
	case Close:
		reason := c.Text
		if reason == "" {
			reason = "closed"
		}
		a.ag.AddEvent(store.EventClosed, map[string]any{"reason": reason}) // 持久事件，与关闭的检查点同事务（06 10.5 表一）
		return true, a.ag.Commit(ctx)
	}
	return false, fmt.Errorf("host: 未知命令 %q", c.Kind)
}

// drainInbox 处理 inbox：通知进状态更新，wake/context 按权限主体合并成一条用户消息后开回合
// （13 13.3）；只有背景/通知时不开回合，等下一次 wake。旧格式 note 仍拼在输入前（H7 / H10）。
//
// 返回 stop=true 表示回合间处理的暂存命令要求 actor 退出（Close / Drain）。每个回合结束后先处理回合中
// 暂存的 Idle 命令，再取下一批：信箱积压时 Suspend / Close 不必等信箱清空（2026-11-04 审查 P1-15）。
func (a *actor) drainInbox(ctx context.Context) (stop bool, err error) {
	defer func() {
		if !stop && err == nil {
			a.replyBarriers(nil)
		}
	}()
	for {
		// 先处理上一回合（含 Recover / Resume 开的回合）暂存的 Idle 命令，再取下一批。
		if stop, err := a.flushQueued(ctx); stop || err != nil {
			return stop, err
		}
		// 预算挂起：信箱保留、不开新回合（02 2.2：补足预算或显式 Resume 后从检查点继续）。
		// 每轮都查：回合间处理的 Suspend 要在下一批之前生效。
		if st, err := a.h.DB.SessionStatus(ctx, a.id); err == nil && st == store.StatusSuspended {
			return false, nil
		}
		items, err := a.h.DB.Inbox(ctx, a.id)
		if err != nil {
			return false, err
		}
		if len(items) == 0 {
			return false, nil
		}
		// 回合开始时已在信箱里的项（快照）：中途插入不取它们，留给下一回合（13 13.3 分轮）。
		turnStart := inboxSet(itemIDs(items))
		// 通知类信封先应用（12：notice 进下一次状态更新），并确认消费。
		if done, err := a.applyNotices(ctx, items); err != nil {
			return false, err
		} else if done {
			a.turnQueued = nil
			continue
		}
		batch := collectInboxBatch(items, a.processing)
		if batch == nil {
			return false, nil
		}
		content, env, untrusted := renderBatch(batch)
		consume := make([]string, len(batch))
		for i, it := range batch {
			consume[i] = it.ItemID
		}
		a.processing = inboxSet(consume) // 本条正在被本回合消费：回合中插入不得重复取它
		a.turnQueued = turnStart         // 回合开始时已在信箱、但不属于本回合的项：中途插入跳过
		for _, id := range consume {     // 本回合消费的项不算「早先排队」——它们就是本条消息
			delete(a.turnQueued, id)
		}
		// 每回合都按本批信封重设 trace：非信封输入（Send/Note）置空，免得沿用上一回合的 trace。
		a.ag.SetTrace(env.Trace)
		// 触发链信息：本回合发出的信封沿用本批里最大的跳数、来源可信度只降不升（08 8.7）。
		a.ag.SetTurnOrigin(agent.TurnOrigin{Hop: batchHop(batch), Untrusted: untrusted})
		a.ag.Ctx.SetUntrusted(untrusted) // 权限组件据此把 allow 降为询问
		if env.Trace != "" {
			a.h.recordTrace(env.Trace, a.id, "session", "")
		}
		m := envelopeMeta(env, untrusted)
		m[session.MetaClientIDs] = consume // 本回合吃进的输入（protocol 2 entry.client_ids、turn.started.inputs）
		if hk := a.h.Hooks.BeforeTurn; hk != nil {
			nc, d, err := hk(a.id, content, untrusted)
			if err != nil {
				d = Deny // Guard FailClosed
			}
			switch d {
			case Deny:
				reason := "denied"
				if err != nil {
					reason = err.Error()
				}
				// inputs / reason 供 protocol 2 的 turn.rejected：被拒的是哪些输入、为什么。
				a.h.publish(a.id, "turn_denied", map[string]any{"unconsumed": content, "inputs": consume, "reason": reason})
				if err := a.ag.Commit(ctx, consume...); err != nil {
					return false, err
				}
				a.processing = nil
				a.turnQueued = nil
				continue
			case RecordOnly:
				if err := a.ag.Core.Record(content, m); err != nil {
					return false, err
				}
				if err := a.ag.Commit(ctx, consume...); err != nil {
					return false, err
				}
				a.processing = nil
				a.turnQueued = nil
				continue
			}
			content = nc
		}
		a.turnInputs = consume        // 守卫放行后才记：被拒的输入不开回合
		a.turnCaps = batchCaps(batch) // 信封 caps 交集：本轮只许调这些工具（空=不限）
		end, terr := a.turn(ctx, func(tctx context.Context) (session.EndReason, error) {
			return a.ag.SendContent(tctx, content, m, consume)
		})
		if terr == nil {
			terr = turnErr(end)
		}
		if terr != nil {
			if a.turnFailed(ctx, terr) {
				return true, terr
			}
		}
		a.processing = nil
		a.turnQueued = nil
		a.turnCaps = nil
	}
}

// flushQueued 处理回合中暂存的 Idle 命令（between 收下、回合内不能执行的），按到达顺序，在 actor 里同步执行。
// barrier（Wait）留到信箱处理完再回复。返回 stop=true 表示其中有命令要求 actor 退出。
func (a *actor) flushQueued(ctx context.Context) (bool, error) {
	a.mu.Lock()
	q := a.queued
	a.queued = nil
	a.mu.Unlock()
	// 回合最后一步之后才到的命令没经过 between，还在队列里：一并按到达顺序取出（不阻塞）。
	for more := true; more; {
		select {
		case c := <-a.in:
			q = append(q, c)
		default:
			more = false
		}
	}
	for i, c := range q {
		if c.Kind == "barrier" {
			a.barriers = append(a.barriers, c)
			continue
		}
		stop, err := a.idle(ctx, c)
		reply(c, err)
		if stop {
			a.mu.Lock() // 退出：剩下的命令放回暂存，exit 关闭时调用方按 ErrClosed 收到
			a.queued = append(q[i+1:len(q):len(q)], a.queued...)
			a.mu.Unlock()
			return true, err
		}
	}
	return false, nil
}

// replyBarriers 回复等待信箱处理完的 Wait。
func (a *actor) replyBarriers(err error) {
	for _, c := range a.barriers {
		reply(c, err)
	}
	a.barriers = nil
}

// mergeSkip 合并两个跳过集合（proc 与 turnQueued）。
func (a *actor) mergeSkip(maps ...map[string]bool) map[string]bool {
	var out map[string]bool
	for _, m := range maps {
		if len(m) == 0 {
			continue
		}
		if out == nil {
			out = make(map[string]bool, len(m))
		}
		for k := range m {
			out[k] = true
		}
	}
	return out
}

// itemIDs 取 inbox 项 id 列表。
func itemIDs(items []store.InboxItem) []string {
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = it.ItemID
	}
	return ids
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
	// 先登记取消函数再排并发槽位：排队期间到达的 Cancel 要能取消这一轮（02 2.4）。
	a.steered = nil
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
	release, err := a.acquireSlot(tctx)
	if err != nil {
		if tctx.Err() != nil && ctx.Err() == nil {
			// 排队时被取消：这一轮不跑，按取消处理（回合已开、需求已发出的交给 drive 收尾）。
			return f(tctx)
		}
		return session.EndReason{}, err
	}
	defer release()
	// trace 事件落库（08 8.7 explain）：回合结束统一写，不参与恢复；用独立 ctx 避免被取消。
	defer func() {
		for _, ev := range a.ag.TakeTraceEvents() {
			_ = a.h.DB.RecordTrace(context.Background(), ev)
		}
	}()
	sp := a.ag.StartSpan("turn", "", "")
	a.ag.TurnSpanID = sp.ID()
	defer func() { a.ag.TurnSpanID = "" }()
	// turn / inputs 供 protocol 2 的 turn.started：回合号与吃进的输入的 client_id。
	a.h.publish(a.id, "turn_start", map[string]any{"turn": a.ag.Core.TurnNumber(), "inputs": a.turnInputs})
	a.turnInputs = nil
	a.ag.SetCaps(a.turnCaps) // 本轮授权范围（信封 caps 交集，T3）
	defer a.ag.SetCaps(nil)
	end, err := f(tctx)
	if err != nil {
		sp.Set("error", err.Error()).End("error")
		return end, err
	}
	sp.Set("end", end.String()).End(end.String())
	a.handBack(ctx)
	if end.Kind == session.EndCancelled {
		a.h.publish(a.id, "turn.interrupted", nil)
	}
	if a.h.Hooks.AfterTurn != nil {
		a.h.Hooks.AfterTurn(a.id, end)
	}
	// turn / end_kind / code / message 供 protocol 2 的结构化 turn.ended（G7）；reason 是 protocol 1 的拼好的字符串。
	a.h.publish(a.id, "turn_end", map[string]any{"reason": end.String(), "turn": a.lastTurn(),
		"end_kind": int(end.Kind), "code": end.Code, "message": end.Message})
	if err := a.ag.Commit(ctx); err != nil { // span 与 after_turn 的修改落库
		return end, err
	}
	// 外发与检查点同事务（08 9.6）：提交成功后再派发；派发在后台进行、失败不影响回合结果，
	// 未标记的外发行会在下次派发时重试。
	a.h.kickFlush()
	return end, nil
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
		_, _ = a.ag.Core.Abort(session.FailedReason("TURN_FAILED"))
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
	// 运行中到达的信封（13 13.3）：notice 进状态更新、wake 在下一次调模型前插入。
	// 投递方只把信封写进 inbox，投递命令可能因 actor 正忙没进队列，这里在步边界兜底。
	// 只在调模型前插入：NeedTools 时本步调用的结果还没写，插进去会拆开调用与结果（R14）。
	if need.Kind == session.NeedModel {
		// 只在有新信时读信箱，读一次给 notice 与插入共用（审查 P1-36）。
		if a.mailDirty.Swap(false) {
			if items, err := a.h.DB.Inbox(context.Background(), a.id); err == nil && len(items) > 0 {
				if _, err := a.applyNotices(context.Background(), items); err != nil {
					return need, err
				}
				if err := a.insertFrom(context.Background(), items); err != nil {
					return need, err
				}
			}
		}
	}
	for {
		var c Command
		select {
		case c = <-a.in:
		default:
			return need, nil
		}
		switch c.Kind {
		case Steer:
			if c.ItemID != "" && a.steered[c.ItemID] {
				reply(c, nil)
				continue
			}
			n, err := a.ag.Core.Steer(steerContent(c), meta(c), steerMode(c.Mode))
			reply(c, err)
			if err == nil && c.ItemID != "" {
				if a.steered == nil {
					a.steered = map[string]bool{}
				}
				a.steered[c.ItemID] = true
			}
			if err == nil && n != nil {
				need = n // WaitingTools 改向：未执行调用补 Redirected，直接进入下一步
			}
		case Deliver:
			// 信封已落 inbox：在下一次调模型前插入（09 9.6）；已到 Done 边界则留给下一回合。
			if need.Kind != session.NeedDone {
				reply(c, a.insertInput(context.Background()))
			} else {
				reply(c, nil)
			}
		case Send, Note:
			// 运行中到达的用户消息：在下一次调模型前插入当前回合（09 9.4）。
			// 已到 Done 边界时留给下一回合，inbox 不动。
			if need.Kind != session.NeedDone {
				reply(c, a.insertInput(context.Background()))
			}
		case SetConfig: // 内核暂存到下一个步边界，随下次 checkpoint 落库
			_, err := a.ag.Core.SetConfig(session.ConfigPatch{MaxSteps: c.MaxSteps, Model: c.Model, Reasoning: c.Reasoning})
			reply(c, err)
		case SetExt: // 只改头部 Ext，随下次 checkpoint 落库；不必等回合结束
			reply(c, a.ag.Core.SetExt(c.Key, c.Value))
		default:
			// Idle 类命令：本回合结束后由 drainInbox 的 flushQueued 按到达顺序同步执行（P1-15）
			a.mu.Lock()
			a.queued = append(a.queued, c)
			a.mu.Unlock()
		}
	}
}

// wireConfirm 把 toolhub 的人工确认接到 human: 地址（11 11.2 D3）：
// 内核签发 corr、登记待回复、发布 human.asked；回答可以是 envelope 回信（corr），
// 也可以是既有的 Confirm 命令（call_id），两条路都落到同一个等待通道。
//
// 等待者的生命周期（C4/C5）：ask 的 ctx 结束（回合取消、截止）或 actor 退出时立即返回；
// 返回时（任何路径）按身份删掉自己登记的等待者，并作废这次签发的 corr——之后到达的
// 迟到回信核对不过，不会被当成普通消息开新回合。
func (a *actor) wireConfirm() {
	a.ag.Hub.SetAwait(a.ag.AwaitFunc(func(ctx context.Context, cx *toolhub.Cx, reason string, deadline time.Time) bool {
		// 登记成 approval 交互（protocol 2，interaction.go）：打开与结束都是持久事件；登记失败时 corr 为空，
		// 只发 call_id，回信路径不可用，Confirm 命令照常。
		iid, corr := a.openInteraction("approval", cx.Tool.Spec.Name+" 需要确认", map[string]any{
			"tool": cx.Tool.Spec.Name, "args": jsonArgs(cx.Args), "reason": reason}, deadline)
		w := make(chan bool, 1)
		a.mu.Lock()
		a.waiters[cx.CallID] = w
		a.mu.Unlock()
		if corr != "" {
			a.h.humanMu.Lock()
			if a.h.humanWait == nil {
				a.h.humanWait = map[string]chan bool{}
			}
			a.h.humanWait[corr] = w
			a.h.humanMu.Unlock()
		}
		outcome := OutcomeVoided
		defer func() {
			a.mu.Lock()
			if a.waiters[cx.CallID] == w { // 按身份删：恢复后同一调用重问时登记的是新的等待者
				delete(a.waiters, cx.CallID)
			}
			a.mu.Unlock()
			if corr != "" {
				a.h.humanMu.Lock()
				delete(a.h.humanWait, corr)
				a.h.humanMu.Unlock()
			}
			// 结束交互（已被 Respond 结束的不变）；没登记成交互时只作废 corr。
			a.closeInteraction(iid, corr, outcome)
		}()
		a.h.publish(a.id, "human_requested", map[string]any{"call_id": cx.CallID, "tool": cx.Tool.Spec.Name,
			"reason": reason, "args": string(cx.Args), "corr": corr, "deadline": deadline.UnixMilli(), "interaction": iid})
		a.h.recordTrace(a.ag.TraceID(), a.id, "envelope", "session:"+a.id+" -> human（确认 "+cx.Tool.Spec.Name+"）")
		select {
		case ok := <-w:
			outcome = OutcomeDenied
			if ok {
				outcome = OutcomeApproved
			}
			return ok
		case <-ctx.Done():
			outcome = waitOutcome(deadline)
			return false
		case <-a.exit:
			return false
		}
	}))
}

func text(s string) session.Content { return session.Content{session.NewTextPart(s)} }

// steerContent 插话内容：带部件时用部件，否则用文本。
func steerContent(c Command) session.Content {
	if len(c.Content) > 0 {
		return c.Content
	}
	return text(c.Text)
}

func meta(c Command) map[string]any {
	src := c.Source
	if src == "" {
		src = "user"
	}
	m := map[string]any{session.MetaSource: src}
	if c.Kind == Steer && c.ItemID != "" {
		m[session.MetaClientIDs] = []string{c.ItemID}
	}
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

// acquireSlot 取一个并发推理许可（Host.Limit 控制）；超出上限时排队等待，不报错（02 2.4）。
func (a *actor) acquireSlot(ctx context.Context) (func(), error) {
	h := a.h
	if h.Limit <= 0 {
		return func() {}, nil
	}
	h.mu.Lock()
	if h.sem == nil { // 首次使用时按 Limit 建，之后不换：换池会让新旧两池的持有者加起来超过上限（审查 P2）
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
	return a.insertFrom(ctx, items)
}

// insertFrom 同 insertInput，用调用方已读到的信箱项（步边界读一次，不再重读）。
// 已被 applyNotices 消费的 notice 不是 input，collectInboxBatch 不会取它们。
func (a *actor) insertFrom(ctx context.Context, items []store.InboxItem) error {
	batch := collectInboxBatch(items, a.mergeSkip(a.processing, a.turnQueued))
	if batch == nil {
		return nil
	}
	content, env, untrusted := renderBatch(batch)
	var ids []string
	for _, it := range batch {
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
	m := envelopeMeta(env, untrusted)
	m[session.MetaClientIDs] = ids
	// 回合中插入的信封同样收窄本轮授权范围（触发链 caps 取交集），不可信输入只升不降。
	a.turnCaps = intersectCaps(a.turnCaps, batchCaps(batch))
	a.ag.SetCaps(a.turnCaps)
	if untrusted {
		a.ag.Ctx.SetUntrusted(true)
		o := a.ag.CurrentTurnOrigin()
		o.Untrusted = true
		a.ag.SetTurnOrigin(o)
	}
	if err := a.ag.Core.MidInput(content, m); err != nil {
		if errors.Is(err, session.ErrNotWaiting) {
			return nil // 回合刚好结束：inbox 留给下一回合
		}
		if errors.Is(err, session.ErrToolsOpen) {
			return nil // 本步工具结果未到齐：留在 inbox，下一个调模型前的边界再插
		}
		return err
	}
	a.ag.Consume(ids...)
	return nil
}

// handBack 把回合结束时没进模型的插话写回信箱（审查 P1-19：原先取消、中断、失败时直接丢掉）。
// 写成补报（note）：拼在下一条输入前、不单独开回合——模型持续失败时不会因回投而反复重跑；
// 同时发 turn.unconsumed 事件，界面可以把文本放回输入框。
func (a *actor) handBack(ctx context.Context) {
	for _, pi := range a.ag.TakeUnconsumed() {
		body, _ := json.Marshal(pi.Content)
		trust := "trusted"
		if v, _ := pi.Meta[session.MetaTrust].(string); v == "untrusted" {
			trust = v
		}
		if _, err := a.h.DB.Admit(context.WithoutCancel(ctx), a.id, store.InboxItem{ItemID: NewItemID(),
			Kind: store.InboxNote, Input: string(body), Trust: trust}); err != nil {
			continue
		}
		a.mailDirty.Store(true)
		a.h.publish(a.id, "turn.unconsumed", map[string]any{"content": pi.Content})
	}
}

// checkCursor 核对会话当前事件游标（protocol 2 if_cursor）：want 为空不核对。在 actor 里读，
// 本会话只有持租约的这个 actor 在写，读到的就是执行这条命令时的游标。
func (a *actor) checkCursor(ctx context.Context, want *int64) error {
	if want == nil {
		return nil
	}
	l, err := LoadHead(ctx, a.h.DB, a.id)
	if err != nil {
		return err
	}
	if l.Cursor != *want {
		return fmt.Errorf("%w: 期望 %d，当前 %d", ErrStale, *want, l.Cursor)
	}
	return nil
}

// turnStart 回合 turn 的起点下标（该回合首条 User 条目）：rewind {to_turn} 截到这里。
// 日志里没有 turn 及之后的回合时返回日志长度（不截）；turn 早于压缩点（起点不在已加载窗口里）返回 ErrBadRewind。
func turnStart(core *session.Core, turn uint32) (int, error) {
	s := core.Snapshot()
	for i, e := range s.Entries {
		if e.Turn < turn {
			continue
		}
		if i == 0 && s.Base > 0 {
			return 0, fmt.Errorf("%w: 回合 %d 早于压缩点 %d", session.ErrBadRewind, turn, s.Base)
		}
		if e.Item.Kind == session.KindUser && (i == 0 || s.Entries[i-1].Turn != e.Turn) {
			return s.Base + i, nil
		}
	}
	return s.Header.LogLen, nil
}

// markCommitted 空闲下 SetExt / SetConfig 提交成功时置 c.Committed（调用方要知道变化是否已落库）。
func markCommitted(c Command, err error) error {
	if err == nil && c.Committed != nil {
		*c.Committed = true
	}
	return err
}

// lastTurn 最后一条条目所属的回合号（回合结束时就是刚结束的回合）。
func (a *actor) lastTurn() uint32 {
	es := a.ag.Core.Snapshot().Entries
	if len(es) == 0 {
		return 0
	}
	return es[len(es)-1].Turn
}
