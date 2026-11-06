// Package wire 应用协议 protocol 2 的线上类型（docs/06 - 应用协议契约.md 第十章）。
//
// 只放线上类型：命令参数、结果、事件载荷、数据类型。全部带 json 标签，键名小写下划线。
// 不 import 任何内核包：内核类型由 appapi 的翻译函数转成这里的类型，内核字段改名不影响协议（10.3）。
// 这里的类型是唯一来源：TS 类型（web/src/wire.ts）由 cmd/wirets 生成。
package wire

import "encoding/json"

// Protocol 本服务端支持的协议版本。
const Protocol = 2

// —— 数据类型（10.3）——

// Entry 一条会话条目。
type Entry struct {
	ID        string   `json:"id"`
	Idx       int      `json:"idx"`
	Turn      uint32   `json:"turn"`
	At        int64    `json:"at"`
	Source    string   `json:"source,omitempty"`
	ClientIDs []string `json:"client_ids,omitempty"`
	Item      Item     `json:"item"`
}

// Item 条目体：平铺的判别联合，Kind 决定哪些字段有效。
//
//	user:        content
//	assistant:   content, reasoning?, tool_calls?, model?, usage?, interrupted?
//	tool_result: call_id, output, status, cancel_reason?, truncated?, output_ref?
//	compaction:  upto, summary
//	context:     source（snapshot / status）, text
//	unknown:     raw
type Item struct {
	Kind string `json:"kind"`

	Content     []Part      `json:"content,omitempty"`
	Reasoning   []Reasoning `json:"reasoning,omitempty"`
	ToolCalls   []ToolCall  `json:"tool_calls,omitempty"`
	Model       string      `json:"model,omitempty"`
	Usage       *Usage      `json:"usage,omitempty"`
	Interrupted bool        `json:"interrupted,omitempty"`

	CallID       string `json:"call_id,omitempty"`
	Output       []Part `json:"output,omitempty"`
	Status       string `json:"status,omitempty"`        // ok / error / cancelled
	CancelReason string `json:"cancel_reason,omitempty"` // turn_cancelled / redirected
	Truncated    bool   `json:"truncated,omitempty"`
	OutputRef    string `json:"output_ref,omitempty"`

	Upto    int    `json:"upto,omitempty"`
	Summary []Part `json:"summary,omitempty"`

	ContextSource string `json:"source,omitempty"` // snapshot / status
	Text          string `json:"text,omitempty"`

	Raw json.RawMessage `json:"raw,omitempty"`
}

// 条目种类。
const (
	KindUser       = "user"
	KindAssistant  = "assistant"
	KindToolResult = "tool_result"
	KindCompaction = "compaction"
	KindContext    = "context"
	KindUnknown    = "unknown"
)

// Part 内容片段。
type Part struct {
	Kind   string    `json:"kind"` // text / media / data / 其他原样
	Text   string    `json:"text,omitempty"`
	Media  *MediaRef `json:"media,omitempty"`
	Data   any       `json:"data,omitempty"`
	Schema string    `json:"schema,omitempty"`
}

// MediaRef 媒体引用：只带引用，字节用 artifact.get 取。
type MediaRef struct {
	Ref    string `json:"ref"`
	MIME   string `json:"mime"`
	Size   uint64 `json:"size,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

// Reasoning 推理文本（不透明部分不上线）。
type Reasoning struct {
	Text string `json:"text"`
}

// ToolCall 工具调用；Args 是 JSON 值，不是字符串。
type ToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// Usage 用量。
type Usage struct {
	Input      uint64 `json:"input"`
	Output     uint64 `json:"output"`
	CacheRead  uint64 `json:"cache_read"`
	CacheWrite uint64 `json:"cache_write"`
	Source     string `json:"source"` // reported / estimated / missing
}

// LogPos 日志位置：base 为最近压缩点，len 为完整日志长度。
type LogPos struct {
	Base int `json:"base"`
	Len  int `json:"len"`
}

// Mode 会话档位。
type Mode struct {
	Mode    string `json:"mode"`
	Label   string `json:"label"`
	Sandbox string `json:"sandbox"`
}

// Settings 会话设置。
type Settings struct {
	MaxSteps uint32 `json:"max_steps"` // 0 = 不限
}

// Interaction 一次等人回答（10.6）。
type Interaction struct {
	ID         string    `json:"id"`
	Session    string    `json:"session"`
	Turn       uint32    `json:"turn,omitempty"`
	Kind       string    `json:"kind"` // approval / question
	Title      string    `json:"title"`
	Approval   *Approval `json:"approval,omitempty"`
	Question   *Question `json:"question,omitempty"`
	Responders []string  `json:"responders"`
	Deadline   int64     `json:"deadline,omitempty"`
	OpenedAt   int64     `json:"opened_at"`
}

// Approval 工具确认的内容。
type Approval struct {
	Tool         string          `json:"tool"`
	Args         json.RawMessage `json:"args,omitempty"`
	Reason       string          `json:"reason,omitempty"`
	SessionRules []string        `json:"session_rules,omitempty"`
	Blocked      []string        `json:"blocked,omitempty"`
}

// Question 提问的内容。
type Question struct {
	Text    string   `json:"text"`
	Options []string `json:"options,omitempty"`
}

// 交互种类与结果。
const (
	InteractionApproval = "approval"
	InteractionQuestion = "question"

	OutcomeApproved = "approved"
	OutcomeDenied   = "denied"
	OutcomeAnswered = "answered"
	OutcomeExpired  = "expired"
	OutcomeVoided   = "voided"
)

// Live 进行中回合当前步已累积的流式状态（快照用，不承诺持久）。
type Live struct {
	Turn    uint32       `json:"turn"`
	Streams []LiveStream `json:"streams"`
	Tools   []LiveTool   `json:"tools"`
}

// LiveStream 一段进行中的输出。
type LiveStream struct {
	StreamID string `json:"stream_id"`
	EntryID  string `json:"entry_id"`
	Part     string `json:"part"` // text / reasoning
	Text     string `json:"text"`
}

// LiveTool 一个进行中的工具调用。
type LiveTool struct {
	CallID string          `json:"call_id"`
	Name   string          `json:"name"`
	Args   json.RawMessage `json:"args,omitempty"`
}

// SessionSnapshot 会话快照：除 live 外全部来自已提交数据，之后的变化一定在 cursor 之后（10.2.2）。
type SessionSnapshot struct {
	ID           string        `json:"id"`
	Parent       string        `json:"parent,omitempty"`
	Origin       string        `json:"origin"`
	Title        string        `json:"title"`
	Pinned       bool          `json:"pinned,omitempty"`
	RunState     string        `json:"run_state"` // idle / running
	Mode         *Mode         `json:"mode,omitempty"`
	Settings     Settings      `json:"settings"`
	Log          LogPos        `json:"log"`
	Interactions []Interaction `json:"interactions"`
	Live         *Live         `json:"live,omitempty"`
	Cursor       int64         `json:"cursor"`
}

// 运行状态。
const (
	RunIdle    = "idle"
	RunRunning = "running"
)

// SessionInfo 会话列表的一行。
type SessionInfo struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Preview   string `json:"preview"`
	RunState  string `json:"run_state"`
	Origin    string `json:"origin"`
	Parent    string `json:"parent,omitempty"`
	UpdatedAt int64  `json:"updated_at"`
	Pinned    bool   `json:"pinned,omitempty"`
}

// Principal 连接身份（10.8）。
type Principal struct {
	ID   string `json:"id"`
	Role string `json:"role"` // viewer / operator
}

// 角色。
const (
	RoleViewer   = "viewer"
	RoleOperator = "operator"
)

// Stats 会话统计。
type Stats struct {
	Attempts    int             `json:"attempts"`
	Reported    int             `json:"reported"`
	Missing     int             `json:"missing"`
	Errors      int             `json:"errors"`
	Input       uint64          `json:"input"`
	Output      uint64          `json:"output"`
	CacheRead   uint64          `json:"cache_read"`
	CacheWrite  uint64          `json:"cache_write"`
	HitRate     float64         `json:"hit_rate"` // -1 = 无数据
	ToolCalls   int             `json:"tool_calls"`
	Compacts    int             `json:"compacts"`
	LastRequest string          `json:"last_request,omitempty"`
	Composition json.RawMessage `json:"composition,omitempty"`
}

// Error 命令错误（10.7）。
type Error struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Retry   bool           `json:"retry"`
	Details map[string]any `json:"details,omitempty"`
}
