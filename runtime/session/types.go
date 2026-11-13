// Package session：会话内核（M4 §3.2 / §5.1–5.3）。
//
// 纯状态机：不做 IO、不开任何点位、不感知存储与协议（S5 §2.2——零改动扩展是它的立身之本）。
// 每次内核调用要么完整生效、要么返回 Err 且状态不变（S1 输入原子性）；核心不 panic（R7）。
//
// 建模沿用 poc-go spike 的已验证形态（S3 结论）：状态枚举 + 转移表；条目判别字段
// kind 区分变体，未识别变体原样保留 raw、restore 拒绝（R15）；快照 JSON round trip。

package session

import (
	"encoding/json"
	"fmt"
)

// Status：内核回合状态（三状态、无 Closed；任何状态都可快照）。
type Status uint8

const (
	StatusIdle Status = iota + 1
	StatusWaitingModel
	StatusWaitingTools
)

func (s Status) String() string {
	switch s {
	case StatusIdle:
		return "Idle"
	case StatusWaitingModel:
		return "WaitingModel"
	case StatusWaitingTools:
		return "WaitingTools"
	default:
		return fmt.Sprintf("status(%d)", uint8(s))
	}
}

func (s Status) MarshalJSON() ([]byte, error) { return json.Marshal(s.String()) }

func (s *Status) UnmarshalJSON(b []byte) error {
	var str string
	if err := json.Unmarshal(b, &str); err != nil {
		return err
	}
	switch str {
	case "Idle":
		*s = StatusIdle
	case "WaitingModel":
		*s = StatusWaitingModel
	case "WaitingTools":
		*s = StatusWaitingTools
	default:
		return fmt.Errorf("%w: 未知内核状态 %q", ErrState, str)
	}
	return nil
}

// EndKind：回合结束原因种类（M4 §3.3 EndReason）。
type EndKind uint8

const (
	EndDone EndKind = iota + 1
	EndCancelled
	EndMaxSteps
	_ // 原 EndDenied（从未产生），留空位保持 EndFailed 的编号
	EndFailed
)

// EndReason：回合结束原因。Failed 带载荷（错误码归 M4 §5.4）。
type EndReason struct {
	Kind    EndKind
	Code    string // Failed：失败原因
	Message string
}

// DoneReason 回合正常完成。
func DoneReason() EndReason { return EndReason{Kind: EndDone} }

// CancelledReason 回合被取消（人中断或应用取消）。
func CancelledReason() EndReason { return EndReason{Kind: EndCancelled} }

// MaxStepsReason 回合因步数达到上限而结束。
func MaxStepsReason() EndReason { return EndReason{Kind: EndMaxSteps} }

// FailedReason 回合因错误失败（code 是稳定错误码）。
func FailedReason(code string) EndReason { return EndReason{Kind: EndFailed, Code: code} }

func (r EndReason) String() string {
	switch r.Kind {
	case EndDone:
		return "Done"
	case EndCancelled:
		return "Cancelled"
	case EndMaxSteps:
		return "MaxSteps"
	case EndFailed:
		return fmt.Sprintf("Failed(%s)", r.Code)
	default:
		return fmt.Sprintf("end(%d)", uint8(r.Kind))
	}
}

// StopReason：模型停止原因（R15：未知值原样保留字符串）。
type StopReason struct {
	V string
}

func (s StopReason) MarshalJSON() ([]byte, error)  { return json.Marshal(s.V) }
func (s *StopReason) UnmarshalJSON(b []byte) error { return json.Unmarshal(b, &s.V) }

// ToolStatus：工具结果状态（M4 §3.3）。
type ToolStatus struct {
	State        string // ok / error / cancelled
	CancelReason string // cancelled 时：TurnCancelled / Redirected
}

var (
	ToolOK            = ToolStatus{State: "ok"}
	ToolError         = ToolStatus{State: "error"}
	ToolTurnCancelled = ToolStatus{State: "cancelled", CancelReason: "TurnCancelled"}
	ToolRedirected    = ToolStatus{State: "cancelled", CancelReason: "Redirected"}
)

// Usage：条目用量（UsageSource：Reported / Estimated / Missing——拿不到标 Missing 不虚构）。
type Usage struct {
	Input      uint64
	Output     uint64
	CacheRead  uint64
	CacheWrite uint64
	Source     string
}

// —— 判别联合 ——//

// Item：条目变体（M4 §3.2）。JSON 形如 {"kind":"user",...}；
// 未识别 kind 原样落 Unknown（R15）。
type Item struct {
	Kind       string          `json:"kind"` // user / assistant / tool_result / compaction / snapshot / status / unknown
	User       *UserItem       `json:"user,omitempty"`
	Assistant  *AssistantItem  `json:"assistant,omitempty"`
	ToolResult *ToolResultItem `json:"tool_result,omitempty"`
	Compaction *CompactionItem `json:"compaction,omitempty"`
	Injection  *InjectionItem  `json:"injection,omitempty"`
	Unknown    *UnknownItem    `json:"unknown,omitempty"`
}

// 条目种类常量。
const (
	KindUser       = "user"
	KindAssistant  = "assistant"
	KindToolResult = "tool_result"
	KindCompaction = "compaction"
	KindSnapshot   = "snapshot" // 上下文快照（L2）：会话开头与压缩后注入
	KindStatus     = "status"   // 状态更新（L3）：每次调模型前按 key 只发变化
	KindUnknown    = "unknown"
)

// UserItem：用户 / 外部输入条目体。
type UserItem struct {
	Content Content
}

// AssistantItem：模型回复条目体。
type AssistantItem struct {
	Reasoning   []ReasoningBlock
	Content     Content
	ToolCalls   []ToolCall
	Model       string
	Usage       Usage
	Interrupted bool
}

// ToolResultItem：工具结果条目体。
type ToolResultItem struct {
	CallID string
	Output Content
	Status ToolStatus
}

// CompactionItem：压缩边界条目体——追加在日志末尾，取代完整日志中 [0, upto) 的内容。
type CompactionItem struct {
	Upto    int
	Summary Content
}

// InjectionItem：上下文注入条目体（框架设计 12.5）。Text 是内核渲染好的注入文本：
// 快照用 <context> 包裹，状态更新用 <status-update> 包裹；已发哈希记在条目 meta。
type InjectionItem struct {
	Text string
}

// UnknownItem：未识别条目，raw 原样保留（R15）；使会话只读。
type UnknownItem struct {
	Raw json.RawMessage
}

// Content：内容片段序列。
type Content []Part

// Part：内容片段变体（kind = text / media / data / unknown；未识别原样保留 raw，R15）。
type Part struct {
	Kind  string    `json:"kind"`
	Text  string    `json:"text,omitempty"`
	Media *MediaRef `json:"media,omitempty"`
	Data  any       `json:"data,omitempty"`
	Raw   any       `json:"raw,omitempty"`
}

// NewTextPart 构造文本片段。
func NewTextPart(s string) Part { return Part{Kind: "text", Text: s} }

// MediaRef：媒体引用（内容本体在 BlobStore）。
type MediaRef struct {
	URI    string
	MIME   string
	Bytes  uint64
	SHA256 string // 可空
}

// ReasoningBlock：推理块。Opaque 为厂商不透明推理块原样保留——带签名的思考必须
// 原样回传，任何调用方（适配器、插件层、宿主）都不得修改——思考只读。
type ReasoningBlock struct {
	Text   *string
	Opaque any
}

// ToolCall：工具调用请求。ID 由内核生成（S6：全局唯一永不复用）；
// WireID 为厂商原始 id，只在构造请求时回传，厂商未给或已用过则等于内核 id。
type ToolCall struct {
	ID     string
	WireID string
	Name   string
	Args   json.RawMessage
}

// ToolSpec：工具规格（config.tools 展开后冻结）。
type ToolSpec struct {
	Name        string
	Description string
	Schema      json.RawMessage
}

// Entry：会话条目——V 条目格式版本，Idx 会话内单调，Kind 为判别字段。
type Entry struct {
	V    uint16         `json:"v"`
	ID   string         `json:"id"`   // 核心生成，全局唯一且按时间有序（UUIDv7）
	Turn uint32         `json:"turn"` // 所属回合序号，从 1 开始
	At   int64          `json:"at"`   // 写入时间，Unix 毫秒
	Meta map[string]any `json:"meta,omitempty"`
	Item Item           `json:"item"`
}

// Meta 键（M4 §3.2 条目元数据：调用方随事实传入、核心原样写入不解释、写入后只读）。
const (
	MetaSource = "source" // user / agent / room / system / timer / tool_task / operator
	MetaFrom   = "from"
	MetaTrust  = "trust" // trusted / untrusted
	MetaSteer  = "steer" // append / redirect
	// MetaClientIDs 条目吃进的输入的 client_id（inbox item id / steer 幂等键，protocol 2 的 entry.client_ids）。
	MetaClientIDs = "client_ids"
)

// —— 配置 ——//

// SystemBlock：系统提示词分段。Breakpoint 标记前缀缓存断点。
// Source 标记分段来源（如 "agent" / "plugin:agents-md" / "plugin:skill"），
// 内核原样透传、不解释，只供上下文组成统计（review-1 G1）。
type SystemBlock struct {
	Text       string
	Breakpoint bool
	Source     string `json:",omitempty"`
}

// ToolChoice：tool_choice 参数。
type ToolChoice struct {
	Mode string // auto / none / required / tool
	Name string // Mode = tool 时
}

// Params：模型参数（有序表，保证序列化确定；厂商透传按顺序落 Extra）。
type Params struct {
	Temperature *float64
	MaxTokens   *uint64
	ToolChoice  *ToolChoice
	Reasoning   *string
	Extra       []Param // 厂商透传，保持声明顺序
}

// Param：单个透传参数。
type Param struct {
	Name  string
	Value any
}

// Config：会话配置（冻结于创建时展开：工具列表会话内不追加不移除）。
type Config struct {
	Model  string        // 模型别名（M3 宿主模型管理器）
	System []SystemBlock // 系统提示词分段
	Tools  []ToolSpec
	Params Params
	Policy Policy
}

// Policy：资源上限（S4；超限即拒不静默截断）。
type Policy struct {
	MaxSteps            uint32 // 单回合最多步数（插话触发的步也计入）；0 = 不限
	MaxToolCallsPerStep uint32 // 单步最多工具调用，超出部分回填错误结果
	MaxResultBytes      uint64 // 单个工具结果上限（兜底；截断应由工具执行侧在提交前完成）
	MaxExtBytes         uint64 // ext / 单条 Entry.meta 的序列化大小上限
}

// DefaultPolicy 返回默认资源上限。
func DefaultPolicy() Policy {
	return Policy{MaxToolCallsPerStep: 16, MaxResultBytes: 1 << 20, MaxExtBytes: 64 << 10}
}

// —— 头部与快照（M4 §3.1 内核持久化格式）——//

// SteerMode：插话模式（追加 / 改写）。
type SteerMode string

const (
	SteerAppend   SteerMode = "append"
	SteerRedirect SteerMode = "redirect"
)

// PendingInput：暂存插话（随头部持久化，T5）。
type PendingInput struct {
	Content Content
	Meta    map[string]any
	Mode    SteerMode
}

// ConfigPatch：暂存配置修改（外部只能改 model / params / max_steps，R23——白名单由宿主强制）。
type ConfigPatch struct {
	Model  *string
	Params *Params
	// MaxSteps：单回合步数上限（0 = 不限）；用户可改，等待态暂存到步边界生效。
	MaxSteps *uint32
	// Reasoning：只改 Params.Reasoning（推理强度，"" = 回到厂商缺省），不动其余参数；用户可改，规则同 MaxSteps。
	// 与 Params 同时给出时先整体替换 Params 再改这一项。
	Reasoning *string
}

// PendingCompact：暂存压缩（外部 compact 只触发压缩插件不接收 summary）。
type PendingCompact struct {
	Summary Content
	Upto    int
	Meta    map[string]any
}

// TurnInfo：回合信息（回合序号、起点、当前步）。
type TurnInfo struct {
	Turn  uint32
	Start int    // 本回合第一条条目的完整日志下标
	Step  uint32 // 当前步（进入下一步前判定 MaxSteps，T19）
}

// Header：内核头部（每次停顿整体覆盖写；R32：不存已收到的工具结果）。
type Header struct {
	Version               uint32
	Config                *Config
	Ext                   map[string]any  // 键按字典序遍历，序列化确定（Go encoding/json 已保证）
	Status                Status          // Idle / WaitingModel / WaitingTools
	Turn                  *TurnInfo       // 回合序号、起点、当前步
	PendingCalls          []ToolCall      // 尚未写入结果的调用（只存调用本身，不存已收到的结果）
	PendingInputs         []PendingInput  // 暂存插话 (Input, SteerMode)
	PendingConfig         []ConfigPatch   // 暂存配置修改
	PendingCompact        *PendingCompact // 暂存压缩
	Redirected            bool            // WaitingModel 时是否已标记改向（T6）
	NextNeedID            uint64          // S2：模型需求递增编号
	PendingModelRequestID string          // WaitingModel 时非空：本次模型调用对外标识，恢复后沿用（S7）
	LogLen                int             // 完整日志条目数（头部条件写入的版本号）
	LastEntryID           string
	CompactionBase        int // 最近一次压缩的 upto（无压缩则为 0）；恢复默认从这里加载
}

// clearPending 清空全部「回合内暂存」字段（回合结束时用）。
//
// 2026-11-12（M4）：这 7 个字段的有效性依赖 Status——只在非 Idle 时有意义，
// 是「一个结构体 + 一个判别字段」的形态。但**不拆子结构体**：Header 直接 json.Marshal
// 存进 sessions.header 列，字段名就是数据库 schema，拆了破坏已有库的兼容性。
//
// 所以把散在 endTurn 里的 7 行清零收进这里，好处是：
//
//	· 将来加「回合内暂存」字段时，只需改这一处（不会漏清）
//	· 「回合结束要清什么」成为一个有名字的概念
//
// 注意 Status 不在本方法里——调用方先做收尾动作（落暂存条目、应用配置），最后才设 Idle。
func (h *Header) clearPending() {
	h.Turn = nil
	h.PendingCalls = nil
	h.PendingInputs = nil
	h.PendingConfig = nil
	h.PendingCompact = nil
	h.Redirected = false
	h.PendingModelRequestID = ""
}

// Snapshot：快照 = 头部 + 条目窗口（M4 §3.1）。
type Snapshot struct {
	Header  Header
	Base    int      // entries[0] 在完整日志中的下标；≤ compaction_base，且须为某次压缩的 upto 或 0
	Entries []*Entry // [base, end)，共享底层数组不复制条目
}
