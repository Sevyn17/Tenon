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

// ProtocolRange 客户端支持的协议版本区间。
type ProtocolRange struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

// HelloArgs hello：客户端自报名字与协议区间。
type HelloArgs struct {
	Client   string        `json:"client"`
	Protocol ProtocolRange `json:"protocol"`
}

// HelloResult hello 的结果：协商出的协议版本、服务端身份、能力清单与运行环境。
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

// SessionArgs 只带一个会话 id 的命令参数（session.get / stats / delete 等）。
type SessionArgs struct {
	Session string `json:"session"`
}

// ListArgs sessions.list 的分页与过滤条件。
type ListArgs struct {
	Cursor int64 `json:"cursor,omitempty"`
	Limit  int   `json:"limit,omitempty"`
	All    bool  `json:"all,omitempty"`
	// Pinned 只列置顶的会话（不分页，最多 limit 条；含子会话与否同 all）。
	Pinned bool `json:"pinned,omitempty"`
	// AllWorkspaces 列所有工作区的会话；缺省只列当前工作区的（会话库是用户级的，多个工作区共用）。
	AllWorkspaces bool `json:"all_workspaces,omitempty"`
}

// FollowupsArgs session.followups：某回合（缺省最后一个结束的回合）之后的建议追问。
type FollowupsArgs struct {
	Session string `json:"session"`
	Turn    uint32 `json:"turn,omitempty"`
}

// FollowupsResult session.followups 的结果：某回合之后的建议追问。
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

// FeedbackListResult feedback.list 的结果。
type FeedbackListResult struct {
	Items []Feedback `json:"items"`
}

// UpdateArgs session.update：只改给了的字段。title 为空串表示清掉标题（界面回退显示 id）。
type UpdateArgs struct {
	Session string  `json:"session"`
	Title   *string `json:"title,omitempty"`
	Pinned  *bool   `json:"pinned,omitempty"`
}

// ListResult sessions.list 的结果：一页会话与下一页游标。
type ListResult struct {
	Items []SessionInfo `json:"items"`
	Next  int64         `json:"next"`
}

// CreateArgs session.create：会话 id、定义名、client_id（同 id 重发返回已建会话）。
type CreateArgs struct {
	Session    string `json:"session,omitempty"`
	Definition string `json:"definition,omitempty"`
	ClientID   string `json:"client_id,omitempty"`
}

// SubscribeArgs session.subscribe：订阅某会话的事件（可含子会话）。
type SubscribeArgs struct {
	Session  string `json:"session"`
	After    *int64 `json:"after,omitempty"`
	Children bool   `json:"children,omitempty"`
}

// EntriesArgs session.entries：按区间或尾部条数取会话条目。
type EntriesArgs struct {
	Session string `json:"session"`
	From    *int   `json:"from,omitempty"`
	To      *int   `json:"to,omitempty"`
	Tail    int    `json:"tail,omitempty"`
}

// EntriesResult session.entries 的结果：条目数组与它们在完整日志里的位置。
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

// SendResult session.send 的结果：受理的回合与条目。
type SendResult struct {
	ClientID string `json:"client_id"`
	Queued   bool   `json:"queued"`
}

// SteerArgs session.steer：回合进行中插话或改向。
type SteerArgs struct {
	Session  string `json:"session"`
	Text     string `json:"text"`
	ClientID string `json:"client_id"`
}

// RewindArgs session.rewind：把日志截断到某回合首条用户输入。
type RewindArgs struct {
	Session  string `json:"session"`
	ToTurn   uint32 `json:"to_turn"`
	IfCursor *int64 `json:"if_cursor,omitempty"`
}

// RewindResult session.rewind 的结果：截断后的日志长度与游标。
type RewindResult struct {
	To int `json:"to"`
}

// CompactArgs session.compact：手动触发一次上下文压缩。
type CompactArgs struct {
	Session  string `json:"session"`
	Upto     *int   `json:"upto,omitempty"`
	IfCursor *int64 `json:"if_cursor,omitempty"`
}

// CompactResult session.compact 的结果：压缩后的日志长度与摘要条目。
type CompactResult struct {
	Summary      string `json:"summary"`
	Upto         int    `json:"upto"`
	TokensBefore int    `json:"tokens_before,omitempty"`
	TokensAfter  int    `json:"tokens_after,omitempty"`
}

// ForkArgs session.fork：把当前会话分叉成新的子会话。
type ForkArgs struct {
	Session  string `json:"session"`
	To       string `json:"to,omitempty"`
	Upto     *int   `json:"upto,omitempty"`
	Origin   string `json:"origin,omitempty"` // fork / side
	IfCursor *int64 `json:"if_cursor,omitempty"`
	ClientID string `json:"client_id,omitempty"`
}

// SettingsArgs session.settings：读会话设置。
type SettingsArgs struct {
	Session  string  `json:"session"`
	MaxSteps *uint32 `json:"max_steps,omitempty"`
	// Model 换模型（models.list 里的 id）；回合中到步边界生效。
	Model *string `json:"model,omitempty"`
	// Reasoning 推理强度：ReasoningEfforts 之一，"" = 回到厂商缺省；回合中到步边界生效。
	Reasoning *string `json:"reasoning,omitempty"`
}

// WorkspaceInfo 一个工作区（项目目录）。workspace.list 的一项、workspace.open 的结果。
type WorkspaceInfo struct {
	Path    string `json:"path"` // 绝对路径
	Name    string `json:"name"` // 最后一级目录名
	Trusted bool   `json:"trusted,omitempty"`
	Current bool   `json:"current,omitempty"`
	// OpenedAt 最近一次打开（毫秒）；0 = 没记录。
	OpenedAt int64 `json:"opened_at,omitempty"`
	// Missing 目录已经不存在（最近列表里的旧记录）。
	Missing bool `json:"missing,omitempty"`
}

// WorkspaceListResult workspace.list：当前工作区在前，其余按最近打开倒序。
type WorkspaceListResult struct {
	Items []WorkspaceInfo `json:"items"`
}

// WorkspaceOpenArgs workspace.open：切到另一个目录。成功后旧工作区的连接会被断开，客户端重连后 hello 报新的 workdir。
type WorkspaceOpenArgs struct {
	Path string `json:"path"`
}

// WorkspaceOpenResult workspace.open 的结果：打开后的工作区路径。
type WorkspaceOpenResult struct {
	Workspace WorkspaceInfo `json:"workspace"`
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
	// Efforts 能设的推理强度（session.settings {reasoning}），从低到高；空 = 这个模型不能调。
	Efforts []string `json:"efforts,omitempty"`
	// Available 提供方凭证可用；为假时 Reason 说明（如缺环境变量），选它会在调用时失败。
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// ModelsListResult models.list 的结果：可用模型清单。
type ModelsListResult struct {
	Models  []ModelInfo `json:"models"`
	Default string      `json:"default"` // 新会话的缺省模型
}

// SettingsResult session.settings 的结果。
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

// RespondArgs interaction.respond：回答一个待应答的交互。
type RespondArgs struct {
	Interaction string   `json:"interaction"`
	Response    Response `json:"response"`
	ClientID    string   `json:"client_id,omitempty"`
}

// RespondResult interaction.respond 的结果。
type RespondResult struct {
	Outcome string `json:"outcome"`
}

// InteractionsResult interactions 的结果：待应答的交互清单。
type InteractionsResult struct {
	Items []Interaction `json:"items"`
}

// ModeInfo 一个权限档位的摘要。
type ModeInfo struct {
	ID      string   `json:"id"`
	Label   string   `json:"label"`
	Short   string   `json:"short,omitempty"` // 工具栏等窄处的简称；空 = 用 Label
	Builtin bool     `json:"builtin"`
	Project bool     `json:"project"`
	Widened []string `json:"widened,omitempty"`
}

// ModeListResult mode.list 的结果：可用档位清单。
type ModeListResult struct {
	Modes []ModeInfo `json:"modes"`
}

// ModeSetArgs mode.set：写一个档位的定义。
type ModeSetArgs struct {
	Session string `json:"session"`
	Mode    string `json:"mode"`
	Confirm bool   `json:"confirm,omitempty"`
}

// ModeSetResult mode.set 的结果。
type ModeSetResult struct {
	State     Mode `json:"state"`
	Committed bool `json:"committed"`
}

// ModeShowArgs mode.show：展示某档位对各权限项的取值。
type ModeShowArgs struct {
	Session string `json:"session,omitempty"`
	Mode    string `json:"mode,omitempty"`
}

// ModeItem mode.show 里一个权限项在该档位下的取值与来源。
type ModeItem struct {
	Item    string `json:"item"`
	Title   string `json:"title"`
	Value   string `json:"value"`
	Source  string `json:"source"`
	Bounded bool   `json:"bounded"`
}

// ModeShowResult mode.show 的结果：按权限项列出的取值与来源。
type ModeShowResult struct {
	Mode  string     `json:"mode"`
	Items []ModeItem `json:"items"`
}

// ArtifactGetArgs artifact.get：按 id 取一段工件内容。
type ArtifactGetArgs struct {
	Session string `json:"session"`
	Ref     string `json:"ref"`
	Offset  int    `json:"offset,omitempty"`
	Length  int    `json:"length,omitempty"`
}

// ArtifactGetResult artifact.get 的结果：base64 内容与元信息。
type ArtifactGetResult struct {
	MIME   string `json:"mime"`
	Size   int    `json:"size"`
	Text   string `json:"text,omitempty"`
	Base64 string `json:"base64,omitempty"`
}

// ArtifactPutArgs artifact.put：写一段工件内容。
type ArtifactPutArgs struct {
	Session string `json:"session"`
	MIME    string `json:"mime"`
	Base64  string `json:"base64"`
}

// ArtifactPutResult artifact.put 的结果：工件 id。
type ArtifactPutResult struct {
	Ref string `json:"ref"`
}

// TraceArgs trace.explain：按 trace id 取一条调用链的说明。
type TraceArgs struct {
	Trace string `json:"trace"`
}

// TraceResult trace.explain 的结果。
type TraceResult struct {
	Text string `json:"text"`
}

// Empty 没有结果内容的命令用它占位。
type Empty struct{}

// FilesArgs workspace.files：query 为空时给最近修改的文件，否则模糊匹配路径。
type FilesArgs struct {
	Query string `json:"query,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

// FileInfo 工作区里的一个文件或目录。
type FileInfo struct {
	Path  string `json:"path"` // 相对工作目录，/ 分隔
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"` // Unix 毫秒
}

// FilesResult workspace.files 的结果：目录下的一页条目。
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

// PluginInfo 一个已装配组件的状态与配置。
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

// PluginsListResult plugins.list 的结果。
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

// PluginsSetResult plugins.set 的结果：写入后的开关状态。
type PluginsSetResult struct {
	Revision string `json:"revision"`
	// Restart 恒为真：插件在进程启动时装配，改动重启后生效。
	Restart bool       `json:"restart"`
	Plugin  PluginInfo `json:"plugin"`
}

// PackageInfo 一个已加载的插件包。
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

// PackagesListResult packages.list 的结果。
type PackagesListResult struct {
	Packages []PackageInfo `json:"packages"`
}
