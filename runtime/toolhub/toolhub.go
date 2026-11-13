// Package toolhub：工具管理器（tool-hub 定稿 D1–D7）。
//
// 调用栈（由外到内）：入口环节 → Transform 层 → Guard 层 → 执行器 → Tool.Call。
//   - 入口：查幂等记录（done 取回 / started 回"状态未知"）、Schema 校验；返回时存超大结果为工件。
//   - 执行器：有副作用的工具先写 started，按超时执行。
//
// 并行（D6）：单一分发者按调用顺序取读写锁（可并行 = 读锁，其余 = 写锁），并发上限 8；
// 结果按调用顺序产出，宿主拿到一个交一个。
package toolhub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Sevyn17/tenon/runtime/callctx"
	"github.com/Sevyn17/tenon/runtime/session"
)

// Risk 风险档。
type Risk int

const (
	Read Risk = iota
	Write
	Dangerous
)

func (r Risk) String() string {
	return [...]string{"read", "write", "dangerous"}[r]
}

// 默认值（D3）。
const (
	DefaultTimeout     = 120 * time.Second
	DefaultMaxParallel = 8
	DefaultFinishGrace = 10 * time.Second
)

// Attrs 约定键。
const (
	AttrPreview = "preview" // head / tail / head_tail（D7）
)

// Tool 一个工具。Call 必填；Cancel 可空（级联取消时调用）。
type Tool struct {
	Spec     session.ToolSpec
	Risk     Risk
	Parallel *bool         // nil = Risk == Read
	Timeout  time.Duration // 0 = DefaultTimeout
	Attrs    map[string]any
	// Items 本工具涉及的权限项 id（见 items.go）。为空且 NoAuth 为假时，判定方一律询问。
	Items []string
	// NoAuth 显式声明无需授权（todo、ask_user 这类只和用户交互的工具）。
	NoAuth bool
	Call   func(ctx context.Context, cx *Cx, args json.RawMessage) (Output, error)
	Cancel func(ctx context.Context, cx *Cx)
}

// WithItems 声明本工具涉及的权限项并返回工具本身（便于链式写法）。
func (t *Tool) WithItems(ids ...string) *Tool {
	t.Items = append(t.Items, ids...)
	return t
}

func (t *Tool) parallel() bool {
	if t.Parallel != nil {
		return *t.Parallel
	}
	return t.Risk == Read
}

func (t *Tool) timeout(def time.Duration) time.Duration {
	if t.Timeout > 0 {
		return t.Timeout
	}
	return def
}

// OutputKind 工具输出种类。
type OutputKind int

const (
	Done    OutputKind = iota // 完成
	Confirm                   // 需人工确认（只能由准入判定或授权决策者返回）
)

// 2026-11-12（H3）：删掉 Pending——全仓零生产者，设计文档也无依据。
// 工具作者写 Output{Kind: Pending} 会得到「状态 ok、内容为空」的成功结果，
// 幂等记录按 Done 落库，正是 Layer.FailOpen 那种「填了但无效」。
// 真需要异步工具就另立独立返回路径，不要塞进 OutputKind。

// Output 工具输出。
type Output struct {
	Kind    OutputKind
	Content session.Content
	Status  session.ToolStatus
	Data    any    // 结构化结果（前端渲染用，可空）
	Reason  string // Confirm 的原因
}

// Text 构造文本完成结果。
func Text(s string) Output {
	return Output{Kind: Done, Content: session.Content{session.NewTextPart(s)}, Status: session.ToolOK}
}

// Failf 构造失败的工具结果（回给模型，不中止回合）。返回的是 Output 不是 error：原名 Errorf 容易误读（审查 P2 命名）。
func Failf(format string, a ...any) Output {
	return Output{Kind: Done, Content: session.Content{session.NewTextPart(fmt.Sprintf(format, a...))}, Status: session.ToolError}
}

// Cx 一次调用的上下文。
type Cx struct {
	// Ctx 本次调用的上下文（框架设计 04 §4.4）：租户、会话、使用配置、trace。
	// 组件从这里读会话相关信息，不按会话保存状态（组件是全局单例）。
	Ctx *callctx.Ctx
	// Hub 本会话的工具管理器（run_code 这类要回到同一调用栈的工具用）。
	Hub       *Hub
	SessionID string
	CallID    string
	Tool      *Tool
	Args      json.RawMessage // 本次调用的原始参数（确认界面与“本次会话内都允许”用）
	Resumed   bool            // 崩溃恢复后的重发
	Toolset   *Toolset        // 本会话的工具视图（可用集与显露集；可空）
	// Items 本工具引用的权限项（hub 按注册表解析；工具未声明时为空，判定方据此询问）。
	Items          []Item
	ChildSessionID string // 委派类工具填写：子会话 id（tool span 记录，预留）
	finished       bool   // 记录已在确认路径里结清
	started        bool   // 执行器已写 started（层短路时为 false）
	// Approved 本次调用已获人工批准，且参数哈希与挂起记录一致（由 hub 设置）。
	//
	// 语义只有一个：**跳过确认，不跳过判定**。读取方只应据此跳过「要不要问人」，
	// 不应据此跳过 deny 判定（拒绝类仍要重判）。
	//
	// 2026-11-12（M5）：原来注释同时承诺「免确认」与「免重判」两种，6 个读取方实际用法不一。
	// 已把 DefaultAuthorizer 里的 `cx.Approved` 分支删掉（它不该关心批准状态——
	// 它的职责是「没装权限组件时的缺省判定」，而批准是确认流程的事）。
	Approved bool
	// Progress 报告执行中的输出（易失，只给界面看，不进结果）：text 是"最新的一段"而不是增量，
	// 界面直接替换显示。nil = 没人看，工具不必采样。
	Progress func(text string)
}

// Next 层的下一环。
type Next func(ctx context.Context, cx *Cx, args json.RawMessage) (Output, error)

// Record 幂等 / 挂起记录的视图。
type Record struct {
	Status   string // started / pending / delivered / done
	Output   string
	Decision string // delivered 时：approve / deny
	ArgsHash string
}

// Store 幂等与挂起记录（宿主以 store/sqlite 实现）。
type Store interface {
	Lookup(ctx context.Context, sessionID, callID string) (Record, bool, error)
	// PutToolStarted 有副作用的执行前写 started；已批准（delivered）的记录推进为 started。
	PutToolStarted(ctx context.Context, sessionID, callID, argsHash string) error
	FinishTool(ctx context.Context, sessionID, callID, from, output string) error
	// PutPending 写等确认的挂起记录；已存在（恢复后重问）不报错。
	PutPending(ctx context.Context, sessionID, callID, argsHash, reason string, deadline time.Time) error
}

// Await 等待人工确认（宿主实现：发 HumanRequested、阻塞到 Confirm 命令或截止）。
// 返回 true = 批准；ctx 取消返回 ctx.Err()。决定在返回前已落库（pending → delivered）。
type Await func(ctx context.Context, cx *Cx, reason string, deadline time.Time) (bool, error)

// DefaultConfirmDeadline 确认默认截止（tool-hub D3）。
const DefaultConfirmDeadline = 24 * time.Hour

// Artifacts 工件存储（store/sqlite.Blobs 实现）。
type Artifacts interface {
	Put(ctx context.Context, sessionID, mime string, data []byte) (string, error)
	Get(hash string) ([]byte, error)
}

// Hub 工具管理器。装配期登记完成后只读。
type Hub struct {
	tools       map[string]*Tool
	order       []string
	transforms  []Transform // 改写层，由外到内
	gates       []Gate      // 准入判定，并行跑
	store       Store
	art         Artifacts
	maxParallel int
	thresholds  Thresholds
	items       map[string]Item
	visible     func(name string) bool // 由权限策略层注入：取值 deny 的工具不进显露集
	onSpan      func(SpanEvent)
	onProgress  func(callID, text string)
	await       Await
	timeout     time.Duration // 工具未声明 Timeout 时的缺省（[kernel] tool_timeout）
	deadline    time.Duration // 确认截止（[kernel] confirm_deadline）
	// authorizer 授权判定者（04 4.3.1）：nil = 没人判定，Hub.Validate 拒绝装配。
	authorizer Authorizer
}

// Thresholds 存工件阈值（D3）。
type Thresholds struct {
	Bytes, Lines int // 先到者为准
	Preview      int // 预览字节
	PageLines    int // read_artifact 每页行数
}

// DefaultThresholds 32 KiB / 1000 行，预览 16 KiB，翻页 1000 行。
var DefaultThresholds = Thresholds{Bytes: 32 << 10, Lines: 1000, Preview: 16 << 10, PageLines: 1000}

// SpanEvent 一次工具调用的观测（G3），由宿主写入 trace_spans。
type SpanEvent struct {
	CallID, Tool, Risk, Status, Guard string
	Start, End                        time.Time
	ResultBytes                       int
	Artifact                          string
	Recovered                         bool
	ChildSessionID                    string // 委派类工具把子会话 id 写到 Cx.ChildSessionID
}

// Options 构造选项。
type Options struct {
	Store       Store
	Artifacts   Artifacts
	MaxParallel int
	Thresholds  *Thresholds
	OnSpan      func(SpanEvent)
	// OnProgress 工具执行中的输出（Cx.Progress 的去处）；nil = 不给工具 Progress。
	OnProgress func(callID, text string)
	Await      Await // 可空：无人工确认时 Confirm 按拒绝处理
	// ToolTimeout 工具缺省超时；0 = DefaultTimeout。ConfirmDeadline 确认截止；0 = DefaultConfirmDeadline。
	ToolTimeout     time.Duration
	ConfirmDeadline time.Duration
}

// New 构造。内置 read_artifact（Artifacts 非空时）。
func New(o Options) *Hub {
	h := &Hub{tools: map[string]*Tool{}, items: map[string]Item{}, store: o.Store, art: o.Artifacts, maxParallel: o.MaxParallel,
		thresholds: DefaultThresholds, onSpan: o.OnSpan, onProgress: o.OnProgress, await: o.Await, timeout: o.ToolTimeout, deadline: o.ConfirmDeadline}
	if h.timeout <= 0 {
		h.timeout = DefaultTimeout
	}
	if h.deadline <= 0 {
		h.deadline = DefaultConfirmDeadline
	}
	if h.maxParallel <= 0 {
		h.maxParallel = DefaultMaxParallel
	}
	if o.Thresholds != nil {
		h.thresholds = *o.Thresholds
	}
	if h.art != nil {
		_ = h.Register(h.readArtifactTool())
	}
	return h
}

// ErrDup 同名工具。
var ErrDup = errors.New("toolhub: 工具重名")

// Register 登记工具（名称合法性 + 同名报错）。
func (h *Hub) Register(t *Tool) error {
	if !validName(t.Spec.Name) {
		return fmt.Errorf("toolhub: 非法工具名 %q", t.Spec.Name)
	}
	if t.Call == nil {
		return fmt.Errorf("toolhub: 工具 %s 缺 Call", t.Spec.Name)
	}
	if _, dup := h.tools[t.Spec.Name]; dup {
		return fmt.Errorf("%w: %s", ErrDup, t.Spec.Name)
	}
	h.tools[t.Spec.Name] = t
	h.order = append(h.order, t.Spec.Name)
	return nil
}

// DefaultAuthorizer 没有装权限组件时的缺省授权层：只读与声明无需授权的工具放行，其余一律询问
// （与"未声明权限项的工具按一律询问"同一口径，失败即收紧）。权限组件提供自己的授权层时不用它。
// 原先没有授权层时会话构造直接报错，Minimal 预设起不了任何会话（2026-11-04 审查 P0-7）。
// DefaultAuthorizer 没有装权限组件时的缺省授权判定者（04 4.3.1）：
// 只读与声明无需授权的工具放行，其余一律询问（与「未声明权限项的工具按一律询问」同一口径，
// 失败即收紧）。
func DefaultAuthorizer() Authorizer {
	return AuthorizerFunc(func(ctx context.Context, cx *Cx, args json.RawMessage) (Decision, error) {
		// 不看 cx.Approved：批准是「确认流程」的事，本函数是「没装权限组件时的缺省判定」。
		// 已批准的调用不会走到这里要确认（exec.go 在 Confirm 分支里先判 cx.Approved）。
		if cx.Tool == nil || cx.Tool.Risk == Read || cx.Tool.NoAuth {
			return Decision{Action: DecisionAllow}, nil
		}
		return Decision{Action: DecisionConfirm,
			Reason: cx.Tool.Spec.Name + " 有副作用，需要确认（未装权限组件）"}, nil
	})
}

// Authorized 层链里是否有负责授权判定的层。
func (h *Hub) Authorized() bool { return h.authorizer != nil }

// Names 已登记工具名（登记顺序，含内核自带的机制工具）。
func (h *Hub) Names() []string { return append([]string(nil), h.order...) }

// Get 按名取工具。
func (h *Hub) Get(name string) (*Tool, bool) {
	t, ok := h.tools[name]
	return t, ok
}

func validName(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// SetAwait 设置人工确认应答者（宿主在会话打开后接入）。
func (h *Hub) SetAwait(a Await) { h.await = a }
