// Package store 定义内核存储接口与共享类型（框架设计 05 §5.5，T1a）。
//
// core/ 只应依赖本包；具体实现（core/store/sqlite、core/store/memstore）在装配处选定。
// 两个实现共用 core/store/storetest 一致性测试，语义以 sqlite 为准。
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

	"ybase-kernel/core/bus"
	"ybase-kernel/core/session"
)

// Tenant 当前租户（单机固定）。
const Tenant = "local"

// ErrConflict：头部条件写失败（log_len 不符）——会话已被其他写者推进，本写者应转只读（session-host D4）。
var ErrConflict = errors.New("store: 头部条件写冲突")

// ErrNotFound：会话不存在。
var ErrNotFound = errors.New("store: 会话不存在")

// ErrStateMismatch：条件更新时记录不在期望状态。
var ErrStateMismatch = errors.New("store: 工具记录状态不符")

// Store 内核用到的全部存储能力。
type Store interface {
	SessionStore
	InboxStore
	ToolResultStore
	DefinitionStore
	// OnCommit 设置提交成功后的持久事件回调；回调在提交的 goroutine 里同步执行，须很快返回。
	OnCommit(f func(bus.Envelope))
	Close() error
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

// ToolResultStore 工具幂等 / 挂起记录（tool-hub D4）。
type ToolResultStore interface {
	PutToolStarted(ctx context.Context, sessionID, callID, argsHash string) error
	PutToolPending(ctx context.Context, r ToolRecord) error
	Deliver(ctx context.Context, sessionID, callID, delivered, decision string) error
	FinishTool(ctx context.Context, sessionID, callID, from, output string) error
	GetTool(ctx context.Context, sessionID, callID string) (*ToolRecord, error)
	PendingTools(ctx context.Context, sessionID string) ([]ToolRecord, error)
	DeleteFinishedTools(ctx context.Context, sessionID string) error
	LookupTool(ctx context.Context, sessionID, callID string) (ToolRecordView, bool, error)
	StartTool(ctx context.Context, sessionID, callID, argsHash string) error
	PendConfirm(ctx context.Context, sessionID, callID, argsHash, reason string, deadline time.Time) error
	Decide(ctx context.Context, sessionID, callID string, approve bool) error
}

// DefinitionStore 定义类数据：每次修改插入新版本，不原地更新。
type DefinitionStore interface {
	// PutDefinition 写入新版本，返回版本号（从 1 起）与内容哈希（body 的 sha256 十六进制）。
	//
	// body 是定义的规范字节串：智能体定义用 agentdef.Encode(files)，因此这里的哈希
	// 与 agentdef.Hash(files) 恒等，会话行记的 agent_hash 直接用它（09 9.3）。
	PutDefinition(ctx context.Context, kind, id, body string) (version int, hash string, err error)
	// GetDefinition 读指定版本；version <= 0 取最新。不存在返回 ErrNotFound。
	GetDefinition(ctx context.Context, kind, id string, version int) (string, int, error)
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
	Suffix         json.RawMessage // 可空：Transform 追加的消息（带来源）
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
	TruncateFrom *int             // 非空：把 idx >= 它的条目移入归档
	Append       []*session.Entry // 从 AppendFrom 开始的新条目
	AppendFrom   int
	Header       session.Header // 新头部（Config 单独按外壳存）
	RunState     string         // idle / running / suspended
	Request      *RequestRecord
	ConsumeInbox []string
	Spans        []Span
	Preview      string
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
	InboxInput = "input" // 开回合的输入
	InboxNote  = "note"  // 补报：拼在下次输入前，不单独开回合
)

// InboxItem 一条待处理输入。Input 为内容 JSON（session.Content）。
type InboxItem struct {
	ItemID string
	Kind   string
	Input  string
	Trust  string
	At     time.Time
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

// ToolRecordView toolhub.Record 的同构（避免 store 依赖 toolhub）。
type ToolRecordView struct {
	Status, Output, Decision, ArgsHash string
}

// 持久事件类型。
const (
	EventEntry  = "entry"  // data: {idx, entry}
	EventRewind = "rewind" // data: {to}
)

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
