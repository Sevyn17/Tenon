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
	// ConflictFiles 文件还原有冲突（changes.revert、session.rewind {restore_files}）：details.conflicts 是 []ChangeConflict。
	ConflictFiles = "files"
)

// 改动范围（changes.*，客户端改进 M9）。
const (
	ChangeScopeSession  = "session"   // 本会话全部改动（缺省）
	ChangeScopeTurn     = "turn"      // 只看 / 只撤第 turn 回合
	ChangeScopeFromTurn = "from_turn" // 第 turn 回合及之后
	// ChangeScopeGit 工作区相对 git HEAD 的未提交改动（含未跟踪文件，不限本会话）。只读：changes.revert 返回 invalid；
	// 只在能力 changes.git 时可用（没有 git 或工作区不在仓库里时 list / diff 返回 unsupported）。
	ChangeScopeGit = "git"
)

// 改动来源（ChangeFile.Sources）。
const (
	ChangeSourceFS    = "fs"    // write_file / edit
	ChangeSourceShell = "shell" // shell / MCP / 扩展进程调用前后扫描发现，或回合末补扫发现的后台 job 改动
	ChangeSourceDrift = "drift" // 工具调用之外的改动（格式化器、用户手改）
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
	// Workspace 当前工作区的键（与 SessionInfo.workspace 同一个值，客户端改进 M7）：缺省列表只列这个工作区的会话。
	// 空 = 宿主不按工作区分（CLI 等），列表不过滤。
	Workspace string `json:"workspace,omitempty"`
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
	// Unassigned 只列没有工作区的会话（记工作区之前建的旧会话；左栏「未归属」分组，客户端改进 M6 决定 10）。
	// 优先于 all_workspaces。服务端报能力 sessions_unassigned 才支持；不支持时返回 unsupported。
	Unassigned bool `json:"unassigned,omitempty"`
	// 以下三项要服务端报能力 sessions.query（客户端改进 M7）；不支持的服务端返回 unsupported（旧服务端会静默忽略，
	// 所以先查能力再用）。
	//
	// Query 非空：只列标题或任一条用户消息（前 200 字）包含它的会话，不区分 ASCII 大小写，% _ \ 按字面匹配。
	// 工作区、all、archived 照常生效；不能和 pinned 同用。
	Query string `json:"query,omitempty"`
	// Parent 非空：只列这个会话的直接子会话（不看工作区与 all：子会话跟随父会话的工作区）。不能和 pinned、unassigned 同用。
	Parent string `json:"parent,omitempty"`
	// Archived 已归档的会话：""（缺省）= 排除，"only" = 只要已归档的，"include" = 都要。
	// 缺省列表、置顶列表都排除已归档的。
	Archived string `json:"archived,omitempty"`
}

// sessions.list 的 archived 取值。
const (
	ArchivedOnly    = "only"
	ArchivedInclude = "include"
)

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
	// Archived 归档（true）或恢复（false），要能力 sessions.query（客户端改进 M7）。归档同时取消置顶；
	// archived:true 与 pinned:true 同给返回 invalid；给已归档的会话置顶返回 conflict（reason archived），
	// 同一次请求带 archived:false 先恢复则可以。往已归档的会话 session.send 会自动恢复。
	Archived *bool `json:"archived,omitempty"`
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
	// Turn 只取这个回合的全部条目（要能力 entries.turn，客户端改进 M8）；与 from / to / tail 互斥。
	// 回合不存在返回 invalid。
	Turn *uint32 `json:"turn,omitempty"`
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
	// RestoreFiles 同时把第 to_turn 回合及之后改过的文件还原（能力 changes；没有时返回 unsupported）。
	// 先演练：有冲突（未 force 时任何冲突，force 时不可强制的冲突）→ conflict（details.reason=files，
	// details.conflicts），日志和文件都不动。截断失败时把文件写回还原前的内容。
	RestoreFiles bool `json:"restore_files,omitempty"`
	// Force 跳过可强制的冲突（界面二次确认后才设）。只在 restore_files 时有意义。
	Force bool `json:"force,omitempty"`
}

// RewindResult session.rewind 的结果：截断后的日志长度与游标。
type RewindResult struct {
	To int `json:"to"`
	// Files restore_files 时还原了的文件（相对工作目录，正斜杠）。
	Files []string `json:"files,omitempty"`
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
	Session string `json:"session"`
	To      string `json:"to,omitempty"`
	Upto    *int   `json:"upto,omitempty"`
	// UptoTurn 分叉到第 N 回合起点（不含第 N 回合），等价于 upto = 第 N 回合首条条目的下标；
	// N = 最后一回合 + 1 时分叉整个会话。要能力 entries.turn（客户端改进 M8）；与 upto 互斥；
	// N < 1 或 N > 最后一回合 + 1 返回 invalid。
	UptoTurn *uint32 `json:"upto_turn,omitempty"`
	Origin   string  `json:"origin,omitempty"` // fork / side
	IfCursor *int64  `json:"if_cursor,omitempty"`
	ClientID string  `json:"client_id,omitempty"`
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

// WorkspaceReloadArgs workspace.reload：在当前目录上重建运行时（重读 profile、插件包、BYOK、登录令牌）。
// 有会话正在跑回合时缺省返回 not_idle（details.running = 会话数，details.sessions = 会话 id）；
// Force 时先取消这些回合再重建。成功后旧连接会被断开，客户端重连后重新 hello。
// 新配置起不来时回到原来的配置，返回 invalid（details.reverted = true，运行时照常可用）。hello 能力 reload。
type WorkspaceReloadArgs struct {
	Force bool `json:"force,omitempty"`
}

// WorkspaceReloadResult workspace.reload 的结果：重建后的工作区。
type WorkspaceReloadResult struct {
	Workspace WorkspaceInfo `json:"workspace"`
}

// ModelInfo models.list 的一项。
type ModelInfo struct {
	ID       string `json:"id"`
	Alias    bool   `json:"alias,omitempty"`
	Provider string `json:"provider"`
	Name     string `json:"name,omitempty"`
	// Label 界面显示名（如「DeepSeek 日常 · deepseek-chat」）；空 = 显示 id。
	Label         string `json:"label,omitempty"`
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
	// Effective 新会话的缺省模型实际落到哪、凭据从哪来、由哪项设置决定；旧服务端没有这个字段。
	Effective *EffectiveModel `json:"effective,omitempty"`
}

// EffectiveModel 当前生效的缺省模型（models.list 的 effective）。
type EffectiveModel struct {
	Model    string `json:"model"`              // 同 ModelsListResult.Default（别名或 provider/model）
	Provider string `json:"provider,omitempty"` // 计划第一项的提供方
	Protocol string `json:"protocol,omitempty"` // openai-chat / anthropic-messages / mortise-gateway ……
	BaseURL  string `json:"base_url,omitempty"`
	Name     string `json:"name,omitempty"`  // 厂商模型名
	Label    string `json:"label,omitempty"` // 界面显示名（同 ModelInfo.Label）
	// CredKind 凭据来源：env（环境变量）、byok（我的模型）、platform（平台账号）、none（不需要凭据）、other。
	CredKind string `json:"cred_kind"`
	// CredLabel 给人看的凭据来源，如「环境变量 DEEPSEEK_API_KEY」「我的模型：DeepSeek 日常」「平台账号」。
	CredLabel string `json:"cred_label,omitempty"`
	// Origin 由哪项设置决定：flag（命令行参数）、env（环境变量）、profile（某层 profile）、auto（自动选择）、
	// builtin（内置缺省）；config = 配置给的但宿主没细分；"" = 应用直接指定。
	Origin string `json:"origin,omitempty"`
	// OriginRef 补充：参数名（-model）、变量名（MORTISE_MODEL）、文件路径，或自动选择的理由。
	OriginRef string `json:"origin_ref,omitempty"`
	// Available 凭据现在可用；为假时 Reason 说明。
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	// Notice 回落提示：显式选的缺省模型解析不了、改用了这个时写明原因；空 = 没有回落。
	Notice string `json:"notice,omitempty"`
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

// ChangesArgs changes.list：一个会话经工具改过的文件（客户端改进 M9，能力 changes）。
type ChangesArgs struct {
	Session string  `json:"session"`
	Scope   string  `json:"scope,omitempty"` // ChangeScope*；空 = session
	Turn    *uint32 `json:"turn,omitempty"`  // scope 为 turn / from_turn 时必填（≥ 1）
}

// ChangeFile 一个文件在所选范围内的净改动。
type ChangeFile struct {
	Path    string   `json:"path"`   // 相对工作目录，正斜杠
	Status  string   `json:"status"` // added | modified | deleted
	Added   int      `json:"added"`  // 增加行数；二进制或内容没存下时为 0
	Removed int      `json:"removed"`
	Binary  bool     `json:"binary,omitempty"`
	Sources []string `json:"sources,omitempty"` // ChangeSource*（fs | shell | drift）；git 范围为空
	Turns   []uint32 `json:"turns,omitempty"`   // git 范围为空
	// Untracked git 范围：未跟踪的新文件（status 为 added）。
	Untracked bool `json:"untracked,omitempty"`
	// Rewound 这个文件的改动所在回合都已被 rewind 截掉（只在 session 范围出现；不可还原）。
	Rewound    bool   `json:"rewound,omitempty"`
	Revertible bool   `json:"revertible"`
	Why        string `json:"why,omitempty"` // 不可还原的原因
	// Conflict 预检：还原时会遇到的可强制冲突种类（ChangeConflict.Reason），空 = 可直接还原。
	Conflict string `json:"conflict,omitempty"`
}

// ChangesResult changes.list 的结果。
type ChangesResult struct {
	Files []ChangeFile `json:"files"`
	// ScanSkipped 非空 = 本会话有 shell / MCP / 扩展进程调用时工作区文件数超过 scan_max_files，没有扫描，
	// 这些工具的改动不在列表里（值是给人看的说明）。git 范围不出现。
	ScanSkipped string `json:"scan_skipped,omitempty"`
}

// ChangesDiffArgs changes.diff：一个文件在所选范围内的 unified diff。只按本会话记录里的内容算，
// path 只用作查找键（本会话没改过这个文件 → not_found）。
type ChangesDiffArgs struct {
	Session string  `json:"session"`
	Path    string  `json:"path"`
	Scope   string  `json:"scope,omitempty"`
	Turn    *uint32 `json:"turn,omitempty"`
}

// ChangesDiffResult changes.diff 的结果。
type ChangesDiffResult struct {
	Path      string `json:"path"`
	Diff      string `json:"diff"` // 带 ---/+++/@@ 头的 unified diff（a/、b/ 前缀，新建为 /dev/null）；二进制为空
	Binary    bool   `json:"binary,omitempty"`
	OldSize   int64  `json:"old_size"`
	NewSize   int64  `json:"new_size"`
	Truncated bool   `json:"truncated,omitempty"` // 超过 5000 行或 512 KiB，只给了前一部分
	// Unavailable 某一侧内容没存下（太大、配额满），给不出 diff 的原因。
	Unavailable string `json:"unavailable,omitempty"`
}

// ChangesRevertArgs changes.revert：把本会话改过的文件还原到范围起点。只还原本会话记录里有的文件。
type ChangesRevertArgs struct {
	Session string   `json:"session"`
	Paths   []string `json:"paths,omitempty"` // 空 = 范围内全部；只用作查找键
	Scope   string   `json:"scope,omitempty"`
	Turn    *uint32  `json:"turn,omitempty"`
	DryRun  bool     `json:"dry_run,omitempty"` // 只演练：返回计划与冲突，不写
	Force   bool     `json:"force,omitempty"`   // 跳过可强制的冲突（界面二次确认后才设）
}

// ChangeConflict 一个不能直接还原的文件。
type ChangeConflict struct {
	Path string `json:"path"`
	// Reason modified（外部改过）| other_session | drift | later_turn | unsaved | unrevertible | other_root | unsafe | lost
	Reason    string `json:"reason"`
	Message   string `json:"message"`      // 给人看的原因
	By        string `json:"by,omitempty"` // other_session 时改过它的会话
	Forceable bool   `json:"forceable"`    // force 能否跳过
}

// ChangesRevertResult changes.revert 的结果。有冲突且没 force 时不写任何文件，返回 conflict
// （details.reason=files，details.conflicts）；dry_run 时冲突放在结果里，不报错。
type ChangesRevertResult struct {
	Reverted  []string         `json:"reverted"` // 还原了（dry_run：将要还原）的文件
	Conflicts []ChangeConflict `json:"conflicts,omitempty"`
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

// UISlot 一条界面贡献：插槽位置 + 声明式视图（形状见 declarative.go 的 DeclNode 或数组）+ 可选的数据来源。
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

// —— 平台账号（protocol 2 的可选能力；应用没接平台时这些命令返回空状态）——

// AccountStatus platform.status 的结果。
//
// Configured=false 表示这个应用没接平台（没配 server 地址），
// 此时除 Operator 外其余字段全空，界面应隐藏登录入口而不是报错。
type AccountStatus struct {
	Configured bool   `json:"configured"`
	ServerURL  string `json:"server_url,omitempty"`
	// ServerSource server 地址从哪来（客户端改进 M3，配置T8）：env（环境变量，界面只读）/ file（应用保存的设置）/
	// none（没配）。旧服务端不填。
	ServerSource string `json:"server_source,omitempty"`
	LoggedIn     bool   `json:"logged_in,omitempty"`
	Tenant       string `json:"tenant,omitempty"`
	Subject      string `json:"subject,omitempty"`
	// Product / Channel / Version 客户端自报的标识（登录时带给平台）。
	Product string `json:"product,omitempty"`
	Channel string `json:"channel,omitempty"`
	Version string `json:"version,omitempty"`
	// Operator 本机配置了运营后台、能调运营接口（客户端改进 M5，配置T11）：界面据此显示运营专用页（如「模型服务」）。
	// 与 server 地址无关，Configured=false 时也可能为 true。hello 的 principal.role 对本地连接恒为 operator，不能用来判断。
	// 旧服务端不填。
	Operator bool `json:"operator,omitempty"`
}

// AccountLoginArgs platform.login 的参数。
//
// 设备码登录是两步：先 Start 拿 user_code 展示给用户，用户确认后 Poll 换令牌。
// 界面按 step 推进，不用自己做状态机。
type AccountLoginArgs struct {
	// Step start / poll。start 返回 user_code 与验证地址；poll 换令牌。
	Step string `json:"step"`
	// DeviceCode 第二步要带（第一步返回的）。
	DeviceCode string `json:"device_code,omitempty"`
}

// AccountLoginResult platform.login 的结果。
type AccountLoginResult struct {
	// Done 登录完成（仅 poll 成功时 true）。
	Done bool `json:"done,omitempty"`
	// UserCode / VerificationURI / ExpiresIn / Interval 仅 start 返回。
	UserCode        string `json:"user_code,omitempty"`
	VerificationURI string `json:"verification_uri,omitempty"`
	ExpiresIn       int    `json:"expires_in,omitempty"`
	Interval        int    `json:"interval,omitempty"`
	// DeviceCode start 返回，poll 时带回。
	DeviceCode string `json:"device_code,omitempty"`
	// Pending 用户还没确认（poll 期间）。
	Pending bool `json:"pending,omitempty"`
	// Status 登录后的账号状态（poll 成功时填）。
	Status *AccountStatus `json:"status,omitempty"`
}

// AccountEntitlements platform.entitlements 的结果：权益摘要给界面展示。
type AccountEntitlements struct {
	// Configured 没接平台时为 false。
	Configured bool `json:"configured"`
	// Cached 这次是从缓存拿的（离线降级）。
	Cached bool `json:"cached,omitempty"`
	// Features / Limits / Aliases 权益内容。
	Features map[string]bool  `json:"features,omitempty"`
	Limits   map[string]int64 `json:"limits,omitempty"`
	Aliases  []string         `json:"aliases,omitempty"`
	TTL      int              `json:"ttl,omitempty"`
	Offline  int              `json:"offline_grace,omitempty"`
	Version  int64            `json:"version,omitempty"`
}

// APIKeyListResult platform.apikey.list 的结果。
type APIKeyListResult struct {
	Configured bool     `json:"configured"`
	Keys       []APIKey `json:"keys"`
}

// APIKey 一把 API 密钥的元数据（**不含明文与哈希**）。
type APIKey struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Prefix     string `json:"prefix"`
	CreatedAt  string `json:"created_at"`
	LastUsedAt string `json:"last_used_at,omitempty"`
	ExpiresAt  string `json:"expires_at,omitempty"`
	Revoked    bool   `json:"revoked"`
}

// APIKeyCreateArgs platform.apikey.create 的参数。
type APIKeyCreateArgs struct {
	Name string `json:"name"`
	// ExpiresIn 秒；0 = 永久。
	ExpiresIn int64 `json:"expires_in,omitempty"`
}

// APIKeyCreateResult platform.apikey.create 的结果。
//
// **Key 只在这个响应里出现一次**，之后只能看到 Prefix。
//
// 字段与 APIKey 平铺（不用嵌入）：TS 生成器会把嵌入变成嵌套对象，
// 而实际 JSON 是展平的，前端拿不到 prefix。
type APIKeyCreateResult struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Prefix     string `json:"prefix"`
	CreatedAt  string `json:"created_at"`
	LastUsedAt string `json:"last_used_at,omitempty"`
	ExpiresAt  string `json:"expires_at,omitempty"`
	Revoked    bool   `json:"revoked"`
	Key        string `json:"key"`
}

// APIKeyRevokeArgs platform.apikey.revoke 的参数。
type APIKeyRevokeArgs struct {
	ID string `json:"id"`
}

// APIKeyRevokeResult platform.apikey.revoke 的结果（空）。
type APIKeyRevokeResult struct{}

// BYOKProfile BYOK 配置的界面视图（**不含明文 key**）。
type BYOKProfile struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Protocol  string `json:"protocol"`
	BaseURL   string `json:"base_url"`
	Model     string `json:"model"`
	MaskedKey string `json:"masked_key"`
	CreatedAt string `json:"created_at"`
	IsDefault bool   `json:"is_default"`
}

// BYOKListResult platform.byok.list 的结果。
type BYOKListResult struct {
	Profiles []BYOKProfile `json:"profiles"`
	// Path 配置文件路径（界面上显示，便于用户手工编辑）。
	Path string `json:"path,omitempty"`
}

// BYOKSaveArgs platform.byok.save 的参数。
//
// **APIKey 只在写入时传**：读回来的是 MaskedKey。
type BYOKSaveArgs struct {
	// ID 非空时按 id 编辑已有配置（能力 byok_edit，客户端改进 M3 配置T6）：APIKey 留空表示不改，名称只和其他配置比重名。
	// 空 = 旧语义（新增；同名同地址同模型视为更新）。
	ID       string `json:"id,omitempty"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	BaseURL  string `json:"base_url"`
	Model    string `json:"model"`
	APIKey   string `json:"api_key"`
}

// BYOKTestArgs platform.byok.test 的参数（能力 byok_test）：测一套配置能不能连通。
//
// 带 ID 时以已保存的那套为底，其余非空字段覆盖（可测改了一半的草稿）；APIKey 留空用已存的 Key。
// 不带 ID 时测表单里的值（没保存的新配置）。请求由后端发：浏览器有 CORS 限制，Key 也不该回到前端。
type BYOKTestArgs struct {
	ID       string `json:"id,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	BaseURL  string `json:"base_url,omitempty"`
	Model    string `json:"model,omitempty"`
	APIKey   string `json:"api_key,omitempty"`
}

// BYOKTestResult platform.byok.test 的结果。连不通不是命令错误：OK=false，ErrorKind 分类，Message 是给人看的说明
// （**不含明文 Key**）。
type BYOKTestResult struct {
	OK bool `json:"ok"`
	// LatencyMS 最后一次请求的耗时（毫秒）。
	LatencyMS int64 `json:"latency_ms,omitempty"`
	// Method 怎么测的：models（列模型）/ request（列模型不支持，发了一次 1 token 的最小请求，会消耗极少量额度）。
	Method string `json:"method,omitempty"`
	// ModelFound 列模型成功时：配置的模型在不在列表里。没列到模型（或走了最小请求）时不填。
	ModelFound *bool `json:"model_found,omitempty"`
	// Models 厂商返回的模型 id（最多 50 个）。
	Models []string `json:"models,omitempty"`
	// ErrorKind 失败分类：invalid（参数不全或格式不对）/ auth（401、403：密钥被拒）/ not_found（404：地址或模型不存在）/
	// network（连不上）/ timeout / tls / rate_limit（429）/ server（5xx）/ other。
	ErrorKind string `json:"error_kind,omitempty"`
	// Status 厂商回的 HTTP 状态码（有的话）。
	Status  int    `json:"status,omitempty"`
	Message string `json:"message"`
}

// PlatformConfigureArgs platform.configure 的参数（能力 platform_configure，客户端改进 M3 配置T8）：保存平台 server 地址。
//
// ServerURL 空 = 清除保存的地址。DryRun 只测连通、不保存（界面上的「测试连接」）；Test 先测、测不通就不保存。
// 环境变量（MORTISE_SERVER_URL 一类）设了地址时应用拒绝保存：环境变量优先，存了也不生效。
type PlatformConfigureArgs struct {
	ServerURL string `json:"server_url"`
	Test      bool   `json:"test,omitempty"`
	DryRun    bool   `json:"dry_run,omitempty"`
}

// PlatformConfigureResult platform.configure 的结果。
type PlatformConfigureResult struct {
	// Saved 地址已写入（DryRun 或测试不通时 false）。
	Saved bool `json:"saved"`
	// LoggedOut 地址变了、清掉了旧地址的登录令牌（令牌属于旧 server，不能带去新地址）。
	LoggedOut bool `json:"logged_out,omitempty"`
	// Test 测了的话：连通结果（同 platform.byok.test 的分类）。
	Test *BYOKTestResult `json:"test,omitempty"`
	// Status 保存后的账号状态（server_url、server_source、登录态）。
	Status AccountStatus `json:"status"`
}

// BYOKSaveResult platform.byok.save 的结果。
type BYOKSaveResult struct {
	Profile BYOKProfile `json:"profile"`
}

// BYOKRemoveArgs platform.byok.remove 的参数。
type BYOKRemoveArgs struct {
	ID string `json:"id"`
}

// BYOKRemoveResult platform.byok.remove 的结果（空）。
type BYOKRemoveResult struct{}

// BYOKSetDefaultArgs platform.byok.setDefault 的参数。
type BYOKSetDefaultArgs struct {
	ID string `json:"id"`
}

// BYOKSetDefaultResult platform.byok.setDefault 的结果（空）。
type BYOKSetDefaultResult struct{}

// AdminModelProvider 平台侧厂商（只读）。
type AdminModelProvider struct {
	ID       string `json:"id"`
	Key      string `json:"key"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	BaseURL  string `json:"base_url"`
	Status   string `json:"status"`
	KeyCount int    `json:"key_count"`
}

// AdminModel 平台侧模型（只读）。
type AdminModel struct {
	ID          string `json:"id"`
	ProviderKey string `json:"provider_key"`
	VendorModel string `json:"vendor_model"`
	Kind        string `json:"kind"`
}

// AdminAlias 平台侧别名（只读）。
type AdminAlias struct {
	Alias     string `json:"alias"`
	Kind      string `json:"kind"`
	PlanID    string `json:"plan_id"`
	Visible   bool   `json:"visible"`
	StepCount int    `json:"step_count"`
}

// AdminRateCard 价目卡（只读）。
type AdminRateCard struct {
	ID      string `json:"id"`
	Version int64  `json:"version"`
	Status  string `json:"status"`
	Rates   string `json:"rates"`
}

// AdminModelsResult platform.adminModels 的结果。
type AdminModelsResult struct {
	Providers []AdminModelProvider `json:"providers"`
	Models    []AdminModel         `json:"models"`
	Aliases   []AdminAlias         `json:"aliases"`
	RateCards []AdminRateCard      `json:"rate_cards"`
	ReadOnly  bool                 `json:"read_only"`
}

// PasswordAuthArgs platform.passwordRegister / platform.passwordLogin 的参数。
type PasswordAuthArgs struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	// Name 只在注册时用（可选）。
	Name string `json:"name,omitempty"`
}
