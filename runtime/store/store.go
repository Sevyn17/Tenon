// Package store 定义内核存储接口与共享类型（框架设计 05 §5.5，T1a）。
//
// core/ 只应依赖本包；具体实现（runtime/store/sqlite、runtime/store/memstore）在装配处选定。
// 两个实现共用 runtime/store/storetest 一致性测试，语义以 sqlite 为准。
// SQL() / Migrate / Blobs 等实现专有能力不在接口上（插件声明 SQL 需求时直接拿 *sqlite.DB）。
// Collection 为 T4 内容，此处不定义。
package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"

	"ybase/runtime/bus"
	"ybase/runtime/session"
)

// Tenant 当前租户（单机固定）。
const Tenant = "local"

// ErrConflict：头部条件写失败（log_len 不符）——会话已被其他写者推进，本写者应转只读（session-host D4）。
var ErrConflict = errors.New("store: 头部条件写冲突")

// ErrNotFound：会话不存在。
var ErrNotFound = errors.New("store: 会话不存在")

// ErrStateMismatch：条件更新时记录不在期望状态。
var ErrStateMismatch = errors.New("store: 工具记录状态不符")

// ErrLeaseLost：本写者不再持有会话写租约（纪元不符）——旧节点的迟到写入被拒（02 2.2 fencing）。
var ErrLeaseLost = errors.New("store: 会话写租约已失效")

// ErrCursorExpired：订阅游标对应的事件已按保留期清理（06 10.2、10.12 Q3），调用方应重新取快照再订阅。
var ErrCursorExpired = errors.New("store: 事件游标已过期")

// 会话生命周期状态（02 2.2）。deleted 由 DeleteSession 置墓碑。
const (
	StatusActive    = "active"
	StatusSleeping  = "sleeping"
	StatusSuspended = "suspended" // 预算挂起：停在检查点、不再开新回合，信箱保留
)

// Store 内核必需的存储能力（2026-11-04 审查 P1-53：去掉 9 个没有调用方的子存储访问器；
// 清理 PruneStore、定义 DefinitionStore 改为可选，实现了就用、没有就跳过）。第三方实现只需满足这里的方法。
type Store interface {
	SessionStore
	LeaseStore
	SleepStore
	InboxStore
	CorrStore
	OutboxStore
	TraceStore
	ToolResultStore
	// OnCommit 设置提交成功后的持久事件回调；回调在提交的 goroutine 里同步执行，须很快返回。
	OnCommit(f func(bus.Envelope))
	Close() error

	// Tx 在一个存储事务里执行 fn：fn 拿到的视图上的写入与最外层一起提交，出错整体回滚。
	// 已在事务里时直接加入外层（一致性套件约束回滚与嵌套，见 storetest）。
	Tx(ctx context.Context, fn func(Store) error) error
}

// SessionStore 会话头部、日志、外壳、请求记录、观测与删除。
type SessionStore interface {
	// CreateSession 新建会话（头部 + 外壳 + 可选初始条目）。同 id 或同会话键已存在返回错误。
	CreateSession(ctx context.Context, m SessionMeta, hdr session.Header, entries []*session.Entry) error
	// Commit 执行一次 checkpoint。条件写失败返回 ErrConflict，整体不生效。
	Commit(ctx context.Context, cp Checkpoint) error
	// Load 读头部 + 外壳，并从 base（默认 compaction_base）起读条目。
	Load(ctx context.Context, id string, base *int) (*Loaded, error)
	// Entries 按完整日志下标读 [from, to)；to < 0 表示到末尾。
	Entries(ctx context.Context, id string, from, to int) ([]*session.Entry, error)
	// Exists 报告会话是否存在（未删除）。
	Exists(ctx context.Context, id string) (bool, error)
	// GetRequest 读请求记录；不存在返回 (nil, nil)。
	GetRequest(ctx context.Context, sessionID, requestID string) (*RequestRecord, error)
	// Shell 按哈希读外壳（Config）。
	Shell(ctx context.Context, hash string) (*session.Config, error)
	// ShellBody 按哈希读外壳原文。不存在返回错误。
	ShellBody(ctx context.Context, hash string) ([]byte, error)
	// DeleteSession 置墓碑 → 依次调用 cleaners → 清理全部数据。回调失败墓碑保留、可重试。
	DeleteSession(ctx context.Context, id string, cleaners []Cleaner) error
	// ListSessions 按更新时间倒序列出未删除会话。
	ListSessions(ctx context.Context, before int64, limit int, topOnly bool) ([]SessionInfo, error)
	// FindSessionByKey 按会话键（定义、创建者、key）查会话 id（13 13.3 B19）。
	// 唯一约束不看状态：墓碑占着键也会返回；调用方自行判断已关闭。不存在返回 ErrNotFound。
	FindSessionByKey(ctx context.Context, agentID, creator, key string) (string, error)
	// SessionStats 汇总会话用量与最近一次请求组成。
	SessionStats(ctx context.Context, sessionID string) (Stats, error)
	// Replay 实现 bus.Source：seq > after 的持久事件（升序）及水位。
	Replay(ctx context.Context, session string, after int64) ([]bus.Envelope, int64, error)
	// Export 把会话全部数据写成 JSONL。
	Export(ctx context.Context, sessionID string, w io.Writer) error
}

// InboxStore 会话输入队列（session-host D2 / D6）。
type InboxStore interface {
	// Admit 准入；true = 新写入，false = 同 id 已存在（含已消费）。
	Admit(ctx context.Context, sessionID string, it InboxItem) (bool, error)
	// Inbox 按准入顺序列出未消费项。
	Inbox(ctx context.Context, sessionID string) ([]InboxItem, error)
	// InInbox 报告某项是否仍待处理。
	InInbox(ctx context.Context, sessionID, itemID string) (bool, error)
}

// LeaseStore 会话写租约（02 2.2：租约带纪元）。
//
// 语义：host 打开会话时抢租约，租约纪元加一；这一轮的所有写入（Commit、检查点、
// 工具结果、信箱确认）都必须带这个纪元，存储只接受与当前纪元相符的写入。
//
// 抢占是"后打开者胜 + 纪元 fencing"：AcquireLease 不看现有持有者、没有到期时间，旧持有者在下一次
// 带纪元的写入时才得到 ErrLeaseLost。fencing 保证正确，但两个进程同时打开同一会话会互相抢、
// 白跑模型调用。v1 只支持单进程独占一个库（docs/03 约束清单）；多节点前要加到期时间，
// 让 Acquire 只抢已释放或已过期的租约、RenewLease 续期（2026-11-04 审查 P1-18）。
type LeaseStore interface {
	// AcquireLease 抢租约：holder 成为持有者并返回新纪元（从 1 起）。会话不存在返回 ErrNotFound。
	AcquireLease(ctx context.Context, sessionID, holder string) (epoch int64, err error)
	// RenewLease 续租：holder 与 epoch 都匹配才成功，否则 ErrLeaseLost。
	RenewLease(ctx context.Context, sessionID, holder string, epoch int64) error
	// ReleaseLease 释放租约：holder/epoch 匹配才清空持有者；不匹配忽略（幂等释放）。
	ReleaseLease(ctx context.Context, sessionID, holder string, epoch int64) error
}

// SleepStore 会话休眠状态（02 2.2）：空闲会话释放进程内对象与租约，只剩存储里的几行；
// 新信封到达时任一节点抢到租约即可唤醒（从检查点恢复）。
type SleepStore interface {
	// SessionStatus 读会话生命周期状态（active / sleeping / deleted）。
	SessionStatus(ctx context.Context, sessionID string) (string, error)
	// SetSessionStatus 条件迁移：当前状态必须等于 from 才改为 to；epoch 非零时还要求持有
	// 该纪元的租约（休眠必须在自己持租约时写，唤醒在抢到租约后写）。不满足返回 ErrStateMismatch。
	SetSessionStatus(ctx context.Context, sessionID, from, to string, epoch int64) error
	// SessionsWithStatus 列出某状态的会话 id（按 id 升序，limit<=0 表示不限）。
	SessionsWithStatus(ctx context.Context, status string, limit int) ([]string, error)
}

// SleepScanner 可选：一次查出有可唤醒信件（input / notice）的休眠会话（审查 P1-37：原先按 id 取前 64 个
// 休眠会话再逐个查信箱，前 64 个没信时后面有信的永远扫不到，还是 N+1 查询）。未实现时宿主退回逐个查。
type SleepScanner interface {
	SleepingWithMail(ctx context.Context, kinds []string, limit int) ([]string, error)
}

// ErrCorrRejected：回信核对失败（不存在、已过期、已用过、发送者不是预期回复方）。
var ErrCorrRejected = errors.New("store: corr 回信核对失败")

// Pending 待回复登记（11 11.2 待回复表 / 13 13.3 D3）：请求时内核签发 corr 并登记，
// 回信带上它回到请求会话；只收第一条。
type Pending struct {
	Corr      string
	SessionID string // 发出请求的会话：回信回到这里
	ReplyTo   string // 预期回复方地址
	Trace     string
	IssuedIdx int   // 签发时该会话的日志长度：Rewind 到更早位置后作废（11 11.2）
	Deadline  int64 // Unix 毫秒，0 = 无期限（超时视为无回复）
	Used      bool
	CreatedAt int64 // Unix 毫秒，登记时由实现写（Prune 按它判断保留期）
}

// CorrStore 待回复表（session_pending）。corr 不可猜，由内核签发。
type CorrStore interface {
	// IssueCorr 登记一条待回复。
	IssueCorr(ctx context.Context, p Pending) error
	// ClaimCorr 核对并占用一条待回复：存在、未过期、未用过、发送者 = 预期回复方；
	// 成功时标记已用并返回登记内容，失败返回 ErrCorrRejected。
	ClaimCorr(ctx context.Context, corr, sessionID, from string) (Pending, error)
	// PendingCorr 读一条登记（不存在返回 ErrNotFound）。
	PendingCorr(ctx context.Context, corr string) (Pending, error)
	// VoidCorrAfter 作废某会话在日志位置 idx 及之后签发的未用 corr（Rewind）；
	// 返回作废条数。
	VoidCorrAfter(ctx context.Context, sessionID string, idx int) (int, error)
	// VoidCorr 作废一条 corr（等它的人已经不等了：确认已由别的路径应答、回合取消、截止）。
	// 已用过或不存在时什么也不做。
	VoidCorr(ctx context.Context, corr string) error
}

// ToolResultStore 工具幂等 / 挂起记录（tool-hub D4）。只留原子操作（审查 P1-54）：读视图、决定送达这类
// 组合放在 agent 的适配层（agent.ToolStore），不要求每个存储实现各写一遍。
type ToolResultStore interface {
	PutToolStarted(ctx context.Context, sessionID, callID, argsHash string) error
	PutToolPending(ctx context.Context, r ToolRecord) error
	Deliver(ctx context.Context, sessionID, callID, delivered, decision string) error
	FinishTool(ctx context.Context, sessionID, callID, from, output string) error
	GetTool(ctx context.Context, sessionID, callID string) (*ToolRecord, error)
	PendingTools(ctx context.Context, sessionID string) ([]ToolRecord, error)
	DeleteFinishedTools(ctx context.Context, sessionID string) error
	// StartTool 执行前写 started：无记录则插入；已批准（delivered）且参数哈希一致则推进为 started，否则 ErrStateMismatch。
	StartTool(ctx context.Context, sessionID, callID, argsHash string) error
	// PendConfirm 写等确认记录；已存在不报错，仍是 pending 时刷新参数哈希与截止（一条原子写）。
	PendConfirm(ctx context.Context, sessionID, callID, argsHash string, deadline time.Time) error
}

// DefinitionStore 可选：定义类数据（存储里的智能体定义，agentdef.StoreSource 用）：每次修改插入新版本，不原地更新。
type DefinitionStore interface {
	// PutDefinition 写入新版本，返回版本号（从 1 起）与内容哈希（body 的 sha256 十六进制）。
	//
	// body 是定义的规范字节串：智能体定义用 agentdef.Encode(files)，因此这里的哈希
	// 与 agentdef.Hash(files) 恒等，会话行记的 agent_hash 直接用它（09 9.3）。
	PutDefinition(ctx context.Context, kind, id, body string) (version int, hash string, err error)
	// GetDefinition 读指定版本；version <= 0 取最新。不存在返回 ErrNotFound。
	GetDefinition(ctx context.Context, kind, id string, version int) (string, int, error)
	// ListDefinitions 列出一个定义类出现过的定义 id（每个 id 一次，按 id 升序）。
	ListDefinitions(ctx context.Context, kind string) ([]string, error)
}

// —— 共享类型 ——//

// SessionMeta 会话行的非头部字段。
type SessionMeta struct {
	ID         string
	AgentID    string
	AgentHash  string // 定义整包内容哈希（框架设计 D1）；空 = 未绑定定义
	Creator    string // 创建者（用户 id；空 = 本机）
	ParentID   string
	Origin     string // new / fork / side / subagent / member
	ForkUpto   int
	Title      string
	SessionKey string          // 会话键；空 = 不带 key（不参与唯一约束）
	Meta       json.RawMessage // 应用写入的业务键（JSON 对象）；空 = {}
	UseConfig  json.RawMessage // 创建时算好的使用配置（JSON 对象）；空 = {}
}

// RequestRecord 结构化请求记录（review-1 R1）。
type RequestRecord struct {
	RequestID      string
	ShellHash      string
	ShellBody      []byte // 可空：外壳 JSON；非空时随提交按内容寻址写入外壳表（R5）
	PrefixBase     int
	PrefixEnd      int
	CompactionView json.RawMessage // 可空：本次请求所用的压缩视图（BuildWithCompaction 的 summary/upto）
	FinalHash      string
	Composition    json.RawMessage // 可空：各类上下文 token 估算（G1）
}

// Span 观测记录（review-1 G2）。
type Span struct {
	ID, Parent, Kind, Name, Status string
	Start, End                     time.Time
	Attrs                          json.RawMessage
}

// Checkpoint 一次停顿要原子提交的全部内容。
type Checkpoint struct {
	SessionID    string
	ExpectLogLen int              // 条件写：存储里当前的 log_len 必须等于它
	Epoch        int64            // 非零：这次写入必须持有该纪元的租约，否则 ErrLeaseLost（02 2.2 fencing）
	TruncateFrom *int             // 非空：把 idx >= 它的条目移入归档
	Append       []*session.Entry // 从 AppendFrom 开始的新条目
	AppendFrom   int
	Header       session.Header // 新头部（Config 单独按外壳存）
	RunState     string         // idle / running / suspended
	Request      *RequestRecord
	ConsumeInbox []string
	Spans        []Span
	Preview      string
	// Outbox 本轮运行中要外发的信封：与检查点同事务写入，提交后再派发（08 9.6）。
	Outbox []OutboxItem
	// Events 本次检查点附带的其他持久事件（档位、设置、运行状态变化等，06 10.2.1）：排在条目事件之后，各占一个游标。
	// 条目追加与回退的事件由存储自己生成，不用放在这里。
	Events []Event
}

// Loaded 读取结果。
type Loaded struct {
	Meta     SessionMeta
	RunState string
	Snapshot session.Snapshot
}

// Cleaner 插件清理回调（OnSessionDelete 点位的实现）。
type Cleaner func(ctx context.Context, sessionID string) error

// Inbox 种类。
const (
	InboxInput   = "input"   // 开回合的输入（wake 信封）
	InboxNote    = "note"    // 补报：拼在下次输入前，不单独开回合
	InboxContext = "context" // 背景：不开回合，等下一次 wake 一起进模型（13 13.3）
	InboxNotice  = "notice"  // 通知：进下一次状态更新（12）
)

// InboxItem 一条待处理输入。Input 为内容 JSON（session.Content）。
type InboxItem struct {
	ItemID string
	Kind   string
	Input  string
	Trust  string
	// Envelope 信封的 JSON（runtime/mail.Envelope）；空 = 旧格式（T1b/T2 的纯文本补报/输入）。
	Envelope string
	At       time.Time
}

// OutboxItem 是一条待外发的信封（08 9.6：外发与检查点同事务、提交后派发）。
type OutboxItem struct {
	ID        string          `json:"id"`
	SessionID string          `json:"session_id"`
	Envelope  json.RawMessage `json:"envelope"`
	CreatedAt int64           `json:"created_at"`
	// Attempts 已尝试派发的次数（ClaimOutbox 每认领一次加一）。
	Attempts int `json:"attempts,omitempty"`
}

// OutboxStore 是会话发件箱。Commit 把 OutboxItem 与检查点写在同一事务里；
// 派发成功后由 MarkOutbox 标记已发，崩溃后未发的仍会被重试。
type OutboxStore interface {
	// Outbox 返回待外发的信封（按创建顺序，limit<=0 不限）。
	Outbox(ctx context.Context, limit int) ([]OutboxItem, error)
	// MarkOutbox 标记一条外发信封已派发。
	MarkOutbox(ctx context.Context, id string) error
	// ClaimOutbox 原子认领最多 limit 条待派发信封（多个派发者 / 多个进程同时派发也不会拿到同一条）：
	// 未认领、或认领已过期（上一个派发者崩溃）的行，记为 holder 认领到 now+lease，尝试次数加一，
	// 按创建顺序返回。派发完调 MarkOutbox；临时失败调 ReleaseOutbox 交还。
	ClaimOutbox(ctx context.Context, holder string, lease time.Duration, limit int) ([]OutboxItem, error)
	// ReleaseOutbox 交还一条认领（临时失败，留给下次重试）；只有认领者本人能交还。
	ReleaseOutbox(ctx context.Context, id, holder string) error
	// DeadOutbox 把一条信封标为死信（永久失败或重试次数用尽）：不再派发，reason 留作排查。
	DeadOutbox(ctx context.Context, id, reason string) error
}

// 工具记录状态。
const (
	ToolStarted   = "started"
	ToolPending   = "pending"
	ToolDelivered = "delivered"
	ToolDone      = "done"
)

// ToolRecord 一条幂等 / 挂起记录。
type ToolRecord struct {
	SessionID string
	CallID    string
	Status    string
	Kind      string // pending 的种类：result / question / confirm
	ArgsHash  string // 批准时锁定的参数哈希
	Decision  string
	Delivered string // 挂起结果已送达、未交内核
	Output    string // 已完成结果（JSON）
	Deadline  time.Time
	TokenHash string
}

// 持久事件类型（会话事件表，06 10.2.1）。entry / rewind 由存储随检查点生成，回放时 data 分别为 {idx, entry}、{to}；
// 其余种类由调用方经 Checkpoint.Events 写入，data 原样回放（json.RawMessage）。
const (
	EventEntry  = "entry"  // data: {idx, entry}
	EventRewind = "rewind" // data: {to}
)

// Event 一条要写进事件表的持久事件。
type Event struct {
	Kind string
	Data json.RawMessage
}

// SessionInfo 会话列表的一行。
type SessionInfo struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Preview   string `json:"preview"`
	RunState  string `json:"run_state"`
	Origin    string `json:"origin"`
	ParentID  string `json:"parent"`
	UpdatedAt int64  `json:"updated_at"`
}

// Stats 会话统计（投影：只读 span 与请求记录，不另存状态）。
type Stats struct {
	Attempts   int    // 模型调用尝试次数
	Reported   int    // 带厂商用量的尝试
	Missing    int    // 拿不到用量的尝试（不计入命中率）
	Errors     int    // 失败尝试
	Input      uint64 // 输入 token（含缓存命中部分）
	Output     uint64
	CacheRead  uint64
	CacheWrite uint64
	ToolCalls  int
	Compacts   int
	// 最近一次请求的上下文组成（估算）。
	Composition json.RawMessage
	LastRequest string
}

// HitRate 缓存命中率 = CacheRead / Input（只算 Reported 尝试）；无数据返回 -1。
func (s Stats) HitRate() float64 {
	if s.Input == 0 {
		return -1
	}
	return float64(s.CacheRead) / float64(s.Input)
}

// AddSpan 把一条 span 计入统计（kind 为 attempt / tool / compact，其余忽略）。两个实现共用，保证口径一致。
func (s *Stats) AddSpan(kind, status, attrs string) {
	switch kind {
	case "tool":
		s.ToolCalls++
		return
	case "compact":
		if status == "ok" {
			s.Compacts++
		}
		return
	case "attempt":
	default:
		return
	}
	s.Attempts++
	if status == "error" {
		s.Errors++
	}
	var in, out, cr, cw uint64
	var src string
	if attrs != "" {
		var m map[string]json.RawMessage
		if json.Unmarshal([]byte(attrs), &m) == nil {
			num := func(k string) uint64 {
				var v uint64
				_ = json.Unmarshal(m[k], &v)
				return v
			}
			in, out, cr, cw = num("input"), num("output"), num("cache_read"), num("cache_write")
			_ = json.Unmarshal(m["usage_source"], &src)
		}
	}
	if src != "Reported" {
		s.Missing++
		return
	}
	s.Reported++
	s.Input += in
	s.Output += out
	s.CacheRead += cr
	s.CacheWrite += cw
}

// ShellHash 计算 Config 的外壳哈希（与存储一致：JSON 的 sha256 十六进制）。
func ShellHash(c *session.Config) (string, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	return HashBytes(b), nil
}

// HashBytes 内容哈希（sha256 十六进制），外壳与定义共用。
func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// PruneStore 可选：过期数据清理（D 组）：只删早于 before、而且已经没用的观测与记账行——
// 已用或已过期的待回复、已派发或死信的外发、trace 事件。会话日志、未用的待回复、
// 未派发的外发一律不动。返回删除的行数。宿主启动时与定期调用（保留期见约束清单）。
type PruneStore interface {
	Prune(ctx context.Context, before time.Time) (int, error)
}

// TraceStore 是 trace 事件表（08 8.7）：一次任务的全部信封共享 trace id，
// explain 按 trace 列出经过的会话、信封、工具与拒绝原因。事件只用于观测，不参与恢复。
type TraceStore interface {
	// RecordTrace 追加一条 trace 事件（At 为零时由实现补当前时间）。
	RecordTrace(ctx context.Context, ev TraceEvent) error
	// TraceEvents 按发生顺序列出某个 trace 的全部事件。
	TraceEvents(ctx context.Context, trace string) ([]TraceEvent, error)
}

// TraceEvent 一条 trace 记录。Kind 约定：envelope / session / tool / guard_denied。
type TraceEvent struct {
	Trace   string
	Session string
	Kind    string
	Detail  string
	At      int64 // 毫秒
}
