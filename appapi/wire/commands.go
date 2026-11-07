package wire

import "encoding/json"

// 错误码（10.7）：按客户端要做的决定划分。
const (
	CodeBadRequest    = "bad_request"
	CodeInvalid       = "invalid"
	CodeNotFound      = "not_found"
	CodeClosed        = "closed"
	CodeNotIdle       = "not_idle"
	CodeBusy          = "busy"
	CodeConflict      = "conflict"
	CodeDenied        = "denied"
	CodeNeedConfirm   = "need_confirm"
	CodeCursorExpired = "cursor_expired"
	CodeUnauthorized  = "unauthorized"
	CodeForbidden     = "forbidden"
	CodeUnsupported   = "unsupported"
	CodeInternal      = "internal"
)

// conflict 的 details.reason。
const (
	ConflictExists   = "exists"
	ConflictAnswered = "answered"
	ConflictStale    = "stale"
	ConflictLeased   = "leased"
)

// —— 命令参数与结果（10.4）——

type ProtocolRange struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

type HelloArgs struct {
	Client   string        `json:"client"`
	Protocol ProtocolRange `json:"protocol"`
}

type HelloResult struct {
	Protocol     int       `json:"protocol"`
	Server       string    `json:"server"`
	Principal    Principal `json:"principal"`
	Capabilities []string  `json:"capabilities"`
	Model        string    `json:"model"`
	Workdir      string    `json:"workdir"`
	Trusted      bool      `json:"trusted"`
	MaxOutput    int       `json:"max_output"` // tool_result 线上截断阈值（字节）
}

type SessionArgs struct {
	Session string `json:"session"`
}

type ListArgs struct {
	Cursor int64 `json:"cursor,omitempty"`
	Limit  int   `json:"limit,omitempty"`
	All    bool  `json:"all,omitempty"`
	// Pinned 只列置顶的会话（不分页，最多 limit 条；含子会话与否同 all）。
	Pinned bool `json:"pinned,omitempty"`
}

// FollowupsArgs session.followups：某回合（缺省最后一个结束的回合）之后的建议追问。
type FollowupsArgs struct {
	Session string `json:"session"`
	Turn    uint32 `json:"turn,omitempty"`
}

type FollowupsResult struct {
	Turn  uint32   `json:"turn"`
	Items []string `json:"items"` // 最多 4 条，每条一句话
}

// FeedbackArgs feedback.set：给一条 assistant 条目打分（up / down），vote 为空串表示撤销；note 可选（≤ 2000 字节）。
type FeedbackArgs struct {
	Session string `json:"session"`
	Entry   string `json:"entry"` // 条目 id
	Vote    string `json:"vote"`  // up / down / ""
	Note    string `json:"note,omitempty"`
}

// Feedback 一条反馈。
type Feedback struct {
	Entry string `json:"entry"`
	Vote  string `json:"vote"`
	Note  string `json:"note,omitempty"`
	By    string `json:"by"`
	At    int64  `json:"at"`
}

type FeedbackListResult struct {
	Items []Feedback `json:"items"`
}

// UpdateArgs session.update：只改给了的字段。title 为空串表示清掉标题（界面回退显示 id）。
type UpdateArgs struct {
	Session string  `json:"session"`
	Title   *string `json:"title,omitempty"`
	Pinned  *bool   `json:"pinned,omitempty"`
}

type ListResult struct {
	Items []SessionInfo `json:"items"`
	Next  int64         `json:"next"`
}

type CreateArgs struct {
	Session    string `json:"session,omitempty"`
	Definition string `json:"definition,omitempty"`
	ClientID   string `json:"client_id,omitempty"`
}

type SubscribeArgs struct {
	Session  string `json:"session"`
	After    *int64 `json:"after,omitempty"`
	Children bool   `json:"children,omitempty"`
}

type EntriesArgs struct {
	Session string `json:"session"`
	From    *int   `json:"from,omitempty"`
	To      *int   `json:"to,omitempty"`
	Tail    int    `json:"tail,omitempty"`
}

type EntriesResult struct {
	Entries []Entry `json:"entries"`
	Base    int     `json:"base"`
	Len     int     `json:"len"`
}

// SendArgs Content 与 Text 二选一。
type SendArgs struct {
	Session  string `json:"session"`
	Content  []Part `json:"content,omitempty"`
	Text     string `json:"text,omitempty"`
	ClientID string `json:"client_id"`
}

type SendResult struct {
	ClientID string `json:"client_id"`
	Queued   bool   `json:"queued"`
}

type SteerArgs struct {
	Session  string `json:"session"`
	Text     string `json:"text"`
	ClientID string `json:"client_id"`
}

type RewindArgs struct {
	Session  string `json:"session"`
	ToTurn   uint32 `json:"to_turn"`
	IfCursor *int64 `json:"if_cursor,omitempty"`
}

type RewindResult struct {
	To int `json:"to"`
}

type CompactArgs struct {
	Session  string `json:"session"`
	Upto     *int   `json:"upto,omitempty"`
	IfCursor *int64 `json:"if_cursor,omitempty"`
}

type CompactResult struct {
	Summary      string `json:"summary"`
	Upto         int    `json:"upto"`
	TokensBefore int    `json:"tokens_before,omitempty"`
	TokensAfter  int    `json:"tokens_after,omitempty"`
}

type ForkArgs struct {
	Session  string `json:"session"`
	To       string `json:"to,omitempty"`
	Upto     *int   `json:"upto,omitempty"`
	Origin   string `json:"origin,omitempty"` // fork / side
	IfCursor *int64 `json:"if_cursor,omitempty"`
	ClientID string `json:"client_id,omitempty"`
}

type SettingsArgs struct {
	Session  string  `json:"session"`
	MaxSteps *uint32 `json:"max_steps,omitempty"`
	// Model 换模型（models.list 里的 id）；回合中到步边界生效。
	Model *string `json:"model,omitempty"`
}

// ModelInfo models.list 的一项。
type ModelInfo struct {
	ID            string `json:"id"`
	Alias         bool   `json:"alias,omitempty"`
	Provider      string `json:"provider"`
	Name          string `json:"name,omitempty"`
	ContextWindow int    `json:"context_window,omitempty"`
	Images        bool   `json:"images,omitempty"`
	Reasoning     bool   `json:"reasoning,omitempty"`
	// Available 提供方凭证可用；为假时 Reason 说明（如缺环境变量），选它会在调用时失败。
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

type ModelsListResult struct {
	Models  []ModelInfo `json:"models"`
	Default string      `json:"default"` // 新会话的缺省模型
}

type SettingsResult struct {
	Settings  Settings `json:"settings"`
	Committed bool     `json:"committed"`
}

// Response interaction.respond 的回答：approval 用 Approve / Scope，question 用 Text，带 schema 的用 Value。
type Response struct {
	Approve *bool           `json:"approve,omitempty"`
	Scope   string          `json:"scope,omitempty"` // once / session
	Text    *string         `json:"text,omitempty"`
	Value   json.RawMessage `json:"value,omitempty"`
}

type RespondArgs struct {
	Interaction string   `json:"interaction"`
	Response    Response `json:"response"`
	ClientID    string   `json:"client_id,omitempty"`
}

type RespondResult struct {
	Outcome string `json:"outcome"`
}

type InteractionsResult struct {
	Items []Interaction `json:"items"`
}

type ModeInfo struct {
	ID      string   `json:"id"`
	Label   string   `json:"label"`
	Builtin bool     `json:"builtin"`
	Project bool     `json:"project"`
	Widened []string `json:"widened,omitempty"`
}

type ModeListResult struct {
	Modes []ModeInfo `json:"modes"`
}

type ModeSetArgs struct {
	Session string `json:"session"`
	Mode    string `json:"mode"`
	Confirm bool   `json:"confirm,omitempty"`
}

type ModeSetResult struct {
	State     Mode `json:"state"`
	Committed bool `json:"committed"`
}

type ModeShowArgs struct {
	Session string `json:"session,omitempty"`
	Mode    string `json:"mode,omitempty"`
}

type ModeItem struct {
	Item    string `json:"item"`
	Title   string `json:"title"`
	Value   string `json:"value"`
	Source  string `json:"source"`
	Bounded bool   `json:"bounded"`
}

type ModeShowResult struct {
	Mode  string     `json:"mode"`
	Items []ModeItem `json:"items"`
}

type ArtifactGetArgs struct {
	Session string `json:"session"`
	Ref     string `json:"ref"`
	Offset  int    `json:"offset,omitempty"`
	Length  int    `json:"length,omitempty"`
}

type ArtifactGetResult struct {
	MIME   string `json:"mime"`
	Size   int    `json:"size"`
	Text   string `json:"text,omitempty"`
	Base64 string `json:"base64,omitempty"`
}

type ArtifactPutArgs struct {
	Session string `json:"session"`
	MIME    string `json:"mime"`
	Base64  string `json:"base64"`
}

type ArtifactPutResult struct {
	Ref string `json:"ref"`
}

type TraceArgs struct {
	Trace string `json:"trace"`
}

type TraceResult struct {
	Text string `json:"text"`
}

type Empty struct{}

// FilesArgs workspace.files：query 为空时给最近修改的文件，否则模糊匹配路径。
type FilesArgs struct {
	Query string `json:"query,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

type FileInfo struct {
	Path  string `json:"path"` // 相对工作目录，/ 分隔
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"` // Unix 毫秒
}

type FilesResult struct {
	Files     []FileInfo `json:"files"`
	Truncated bool       `json:"truncated"` // 工作区文件太多，只看了前一部分
}

// ConfigField 插件配置项的 Schema（plugins.list）。
type ConfigField struct {
	Key         string `json:"key"`
	Type        string `json:"type"`
	Default     string `json:"default,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Group       string `json:"group,omitempty"`
	Widget      string `json:"widget,omitempty"`
}

type PluginInfo struct {
	Name    string        `json:"name"`
	On      bool          `json:"on"`
	Default bool          `json:"default"`
	Source  string        `json:"source"`
	Effects []string      `json:"effects"`
	Schema  []ConfigField `json:"schema"`
	// Config 当前进程生效的配置值（profile 合并后，未写的键不出现）。
	Config map[string]any `json:"config"`
	// Mandatory 强制插件：不能停用。
	Mandatory bool `json:"mandatory,omitempty"`
	// Pending 改过、要重启才生效（按 plugins.set 写入的文件重新合并的结果与当前进程不同）。
	Pending *PluginPending `json:"pending,omitempty"`
}

// PluginPending 重启后的开关与配置。
type PluginPending struct {
	On     bool           `json:"on"`
	Config map[string]any `json:"config"`
}

// ProfileScope 可写的一层 profile。
type ProfileScope struct {
	Scope    string `json:"scope"` // user / project
	Path     string `json:"path"`
	Revision string `json:"revision"` // 文件内容哈希；文件不存在为空串
	// Writable 为假时 plugins.set 不能写这一层（项目未受信），Reason 说明原因。
	Writable bool   `json:"writable"`
	Reason   string `json:"reason,omitempty"`
}

type PluginsListResult struct {
	Plugins []PluginInfo `json:"plugins"`
	// Scopes 可写的 profile 层（没有时 plugins.set 返回 unsupported）。
	Scopes []ProfileScope `json:"scopes"`
	// Denied 项目层不能设的键（插件名 → 键），写项目层时会被拒绝。
	Denied map[string][]string `json:"denied,omitempty"`
}

// PluginsSetArgs 改一层 profile 里某插件的开关与配置：enabled 缺省不改；config 的键合并（值为 null 删除该键，
// 回到下层或缺省）。if_revision 是读到的那一层 revision，文件之后被改过返回 conflict（stale）。重启后生效。
type PluginsSetArgs struct {
	Name       string         `json:"name"`
	Enabled    *bool          `json:"enabled,omitempty"`
	Config     map[string]any `json:"config,omitempty"`
	Scope      string         `json:"scope"` // user / project
	IfRevision *string        `json:"if_revision,omitempty"`
}

type PluginsSetResult struct {
	Revision string `json:"revision"`
	// Restart 恒为真：插件在进程启动时装配，改动重启后生效。
	Restart bool       `json:"restart"`
	Plugin  PluginInfo `json:"plugin"`
}

type PackageInfo struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	Scope       string `json:"scope"` // user / project
	Source      string `json:"source"`
	Contents    string `json:"contents"`
	// UI 包的界面贡献（[[ui]] 段）：前端按它往插槽放声明式视图。
	UI []UISlot `json:"ui,omitempty"`
}

// UISlot 一条界面贡献：插槽位置 + 声明式视图（web/src/ui/declarative.tsx 的 DeclNode 或数组）+ 可选的数据来源。
// 视图里的 {{路径}} 用 data 命令的结果替换（{{session}} = 插槽所在会话）。
type UISlot struct {
	ID       string          `json:"id"`
	Slot     string          `json:"slot"`
	Title    string          `json:"title,omitempty"`
	Order    int             `json:"order,omitempty"`
	Requires string          `json:"requires,omitempty"` // 这个后端插件开着才显示
	View     json.RawMessage `json:"view"`
	Data     *UIData         `json:"data,omitempty"`
}

// UIData 视图的数据来源：一条只读协议命令（args 里的 {{session}} 换成插槽所在会话）。
type UIData struct {
	Cmd  string         `json:"cmd"`
	Args map[string]any `json:"args,omitempty"`
}

type PackagesListResult struct {
	Packages []PackageInfo `json:"packages"`
}
