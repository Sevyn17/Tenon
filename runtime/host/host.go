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
	"github.com/Sevyn17/tenon/runtime/agentdef"
	"github.com/Sevyn17/tenon/runtime/bus"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
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
	// Definitions 智能体定义来源（T5a）：投递 agent:<名> 时查该定义是否常驻。
	// nil = 不查（按非常驻处理）。
	Definitions agentdef.Source
	// resident / residentMu 常驻判定缓存（定义来源运行期不变，避免每次投递全量 List）。
	residentMu sync.Mutex
	resident   map[string]bool
	// Remote 出站传输（T6a）：投 remote:<host>/agent:<名> 时用它。nil = 不支持远端。
	Remote RemoteSender
	// RemoteEvents 跨机器事件续读（T6a）：按序号从对端拉事件。nil = 不支持。
	RemoteEvents RemoteEventFetcher
	// DefaultBudget 宿主级预算上限（token；0 = 不限）。会话没配 [use.budget] 时用它兜底。
	DefaultBudget uint64

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
	// MaxHop 单封信封允许的最大跳数（08 8.4）；<=0 用内核缺省 MaxHop（8）。租户可调小（T4f）。
	MaxHop int
	// GuardDeliver / GuardSessionOpen 两个点位的判定区（04 4.3.2），装配期从 Assembly 取来。
	// nil = 没有登记判定（恒 Abstain）。
	//
	// 2026-11-12：原 `Guards []component.GuardRule` 删除——`cx.Guard()` 生产零登记，
	// 整套 `GuardRule`/`EvalGuards` 空转；判定统一走 `WrapGuard` + `EvalPoint`。
	GuardDeliver     func(ctx context.Context, req component.GuardRequest) (component.Verdict, string)
	GuardSessionOpen func(ctx context.Context, req component.GuardRequest) (component.Verdict, string)
	// DeliverChain 投递中间件链（04 4.3 Deliver 点位），由装配层用 SetDeliverMiddleware 装。
	// nil = 没有中间件（直接走内核投递）。中间件在最外层，改写后的信封仍要过守卫。
	DeliverChain func(ctx context.Context, req component.DeliverReq) (component.DeliverResp, error)
	// SessionOpenChain 新建会话中间件链（04 4.3 SessionOpen 点位），装配期装。
	// nil = 没有中间件。只在新建时跑，打开已有会话不跑。
	SessionOpenChain func(ctx context.Context, req component.SessionOpenReq) (component.SessionOpenResp, error)
	// TurnChain 回合中间件链的**构造器**（04 4.3 Turn 点位）：调用时注入本轮内核 kernel，
	// 返回包好中间件的链。nil = 没有中间件。**不跨挂起**：恢复时链重新构造并跑一次，Resumed=true。
	TurnChain func(kernel func(context.Context, component.TurnReq) (component.TurnResp, error)) func(context.Context, component.TurnReq) (component.TurnResp, error)

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
		// SessionOpen 中间件（04 4.3）：只在新建时跑，可写缺省 meta 或拒绝创建。
		if h.SessionOpenChain != nil {
			req := component.SessionOpenReq{SessionID: id, Agent: def, Origin: "new"}
			resp, err := h.SessionOpenChain(ctx, req)
			if err != nil {
				return Handle{}, err
			}
			if resp.Denied {
				_ = h.DB.DeleteSession(ctx, id, nil) // 回滚：不留下半建的会话
				reason := resp.Reason
				if reason == "" {
					reason = "被中间件拒绝"
				}
				return Handle{}, fmt.Errorf("%w: %s", ErrNotAllowed, reason)
			}
		}
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

// Publish 供组件发布一条易失事件（界面订阅用；不落库）。id 可空。
func (h *Host) Publish(id, kind string, data any) { h.publish(id, kind, data) }

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
	// turnBudgetRef 是本轮的预算账（信封 budget_ref，取本批主触发者）：turn 开始时设，结束时按本回合
	// 用量记账（08 8.4）。
	turnBudgetRef string
	// budgetedTurn 已记账的回合号：同一回合重跑（TreePolicy=restart）不重复记账（进程内去重）。
	budgetedTurn uint32
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
