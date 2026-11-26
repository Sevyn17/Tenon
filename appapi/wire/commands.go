package wire

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

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

// invalid 的 details.reason。
const (
	// InvalidOtherWorkspace 会话属于另一个工作区（不是宿主当前工作区）：会触发执行、改运行配置、改文件的命令被拒绝。
	// details.workspace、details.workspace_id 是会话所属工作区；客户端提示切换后再操作。
	InvalidOtherWorkspace = "other_workspace"
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
	// WorkspaceID 当前工作区的稳定 id（WorkspaceID(workspace)，Mortise docs/72 B2）；workspace 为空时不给。
	WorkspaceID string `json:"workspace_id,omitempty"`
	// Extensions 装配的产品扩展（docs/61 3.2）；没有扩展时不给，旧客户端忽略。
	Extensions []ExtensionInfo `json:"extensions,omitempty"`
}

// ExtensionInfo hello.extensions 的一项：产品扩展的命名空间与版本。
type ExtensionInfo struct {
	Namespace string `json:"namespace"`
	Version   string `json:"version,omitempty"`
}

// SessionArgs 只带一个会话 id 的命令参数（session.get / stats / delete 等）。
type SessionArgs struct {
	Session string `json:"session"`
	// AllWorkspaces 读别的工作区的会话（docs/63 W-1）：只对 operator 生效，缺省只读当前工作区。
	AllWorkspaces bool `json:"all_workspaces,omitempty"`
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
	// 以下三项要服务端报能力 sessions.query（客户端改进 M7）；不支持的服务端返回 unsupported（旧服务端会静默忽略，
	// 所以先查能力再用）。
	//
	// Query 非空：只列标题或任一条用户消息（前 200 字）包含它的会话，不区分 ASCII 大小写，% _ \ 按字面匹配。
	// 工作区、all、archived 照常生效；不能和 pinned 同用。
	Query string `json:"query,omitempty"`
	// Parent 非空：只列这个会话的直接子会话（不看工作区与 all：子会话跟随父会话的工作区）。不能和 pinned 同用。
	Parent string `json:"parent,omitempty"`
	// Archived 已归档的会话：""（缺省）= 排除，"only" = 只要已归档的，"include" = 都要。
	// 缺省列表、置顶列表都排除已归档的。
	Archived string `json:"archived,omitempty"`
	// Agent 非空：只列绑定这个定义名的会话（要能力 sessions.query）。工作区、all、archived 照常生效。
	Agent string `json:"agent,omitempty"`
	// WorkspaceID 非空：只列属于这个工作区（WorkspaceID(workspace) 等于它）的会话，优先于 all_workspaces。
	// 不是当前工作区时等同跨工作区读，只给 operator（Mortise docs/74 W4a）。找不到对应工作区时返回空列表。
	WorkspaceID string `json:"workspace_id,omitempty"`
	// ExcludeOrigins 非空：不列 origin 在其中的会话（如产品侧栏排除复盘类会话）。
	// 置顶列表同样生效；要能力 sessions.query，不支持的服务端返回 unsupported。
	ExcludeOrigins []string `json:"exclude_origins,omitempty"`
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
	// AllWorkspaces 读别的工作区的会话（docs/63 W-1）：只对 operator 生效，缺省只读当前工作区。
	AllWorkspaces bool `json:"all_workspaces,omitempty"`
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
	// Source / Ref 会话来源与来源内的引用（如 automation 与任务 id，docs/57 4.2 钩子 3）：记在会话元数据里，
	// 快照与列表行带回。各不超过 128 字节、不含控制字符。
	Source string `json:"source,omitempty"`
	Ref    string `json:"ref,omitempty"`
	// Title 初始标题（同 session.update 的规则）；给了就不再用第一条消息兜底起标题。
	Title string `json:"title,omitempty"`
}

// SubscribeArgs session.subscribe：订阅某会话的事件（可含子会话）。
type SubscribeArgs struct {
	Session  string `json:"session"`
	After    *int64 `json:"after,omitempty"`
	Children bool   `json:"children,omitempty"`
	// AllWorkspaces 读别的工作区的会话（docs/63 W-1）：只对 operator 生效，缺省只读当前工作区。
	AllWorkspaces bool `json:"all_workspaces,omitempty"`
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
	// AllWorkspaces 读别的工作区的会话（docs/63 W-1）：只对 operator 生效，缺省只读当前工作区。
	AllWorkspaces bool `json:"all_workspaces,omitempty"`
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
	// Queue 排队（hello 能力 send.queue，客户端改进 M12 协议T20）：回合进行中不插进当前回合，
	// 回合结束后单独开回合；空闲时与普通发送相同。旧服务端忽略这个字段（按插入处理）。
	Queue bool `json:"queue,omitempty"`
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

// UnqueueArgs session.unqueue：取消一条排队的输入（能力 send.queue）。ID 是发送时的 client_id。
type UnqueueArgs struct {
	Session string `json:"session"`
	ID      string `json:"id"`
}

// UnqueueResult session.unqueue 的结果：removed=false 表示没有这条、已开始处理或已取消。
type UnqueueResult struct {
	Removed bool `json:"removed"`
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
	// WorkMode 工作方式：execute / plan（客户端改进 M12，能力 work_mode）。立即生效（回合中到下一步）；不改权限档位。
	WorkMode *string `json:"work_mode,omitempty"`
}

// WorkspaceInfo 一个工作区（项目目录）。workspace.list 的一项、workspace.open 的结果。
type WorkspaceInfo struct {
	// ID 稳定 id（WorkspaceID：工作区键的 sha256 前 16 位十六进制）。地址栏、链接用它，不暴露路径；目录改名即新工作区。
	ID      string `json:"id"`
	Path    string `json:"path"` // 绝对路径
	Name    string `json:"name"` // 最后一级目录名
	Trusted bool   `json:"trusted,omitempty"`
	Current bool   `json:"current,omitempty"`
	// OpenedAt 最近一次打开（毫秒）；0 = 没记录。
	OpenedAt int64 `json:"opened_at,omitempty"`
	// Missing 目录已经不存在（最近列表里的旧记录）。
	Missing bool `json:"missing,omitempty"`
}

// WorkspaceID 工作区键（SessionInfo.workspace、hello.workspace 同一个值）的稳定 id：sha256 前 16 位十六进制。空键返回空。
func WorkspaceID(key string) string {
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:8])
}

// WorkspaceListResult workspace.list：当前工作区在前，其余按最近打开倒序。
type WorkspaceListResult struct {
	Items []WorkspaceInfo `json:"items"`
}

// LocateArgs session.locate（Mortise docs/72 B2）：会话属于哪个工作区；给了 entry 时再报它所在的回合。
// 只给本机操作者（同 sessions.list {all_workspaces}）：会话不必属于当前工作区。
type LocateArgs struct {
	Session string `json:"session"`
	Entry   string `json:"entry,omitempty"`
}

// LocateResult session.locate 的结果。Workspace 是会话行的工作区键（空 = 不属于任何工作区），WorkspaceID 是它的稳定 id
// （WorkspaceInfo.id 同一算法）。给了 entry 才有 Turn / Rewound：Rewound = 条目已被回退移进归档；条目不存在返回 not_found。
type LocateResult struct {
	Workspace   string `json:"workspace"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	Turn        uint32 `json:"turn,omitempty"`
	Rewound     bool   `json:"rewound,omitempty"`
}

// WorkspaceOpenArgs workspace.open：切到另一个目录。成功后旧工作区的连接会被断开，客户端重连后 hello 报新的 workdir。
type WorkspaceOpenArgs struct {
	Path string `json:"path"`
}

// WorkspaceOpenResult workspace.open 的结果：打开后的工作区路径。
type WorkspaceOpenResult struct {
	Workspace WorkspaceInfo `json:"workspace"`
}

// WorkspaceReloadArgs workspace.reload：在当前目录上重建运行时（重读 profile、插件包、插件读的外部配置）。
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

// 项目信任级别（workspace.trust 的 level；trust.toml 里的取值）。
const (
	TrustTrusted   = "trusted"
	TrustUntrusted = "untrusted"
)

// WorkspaceTrustPreviewResult workspace.trustPreview：当前工作区受信后会生效的项目配置（只读空跑，不启动任何东西），
// 以及当前的信任状态。已受信时列的是正在生效的内容。hello 能力 workspace.trust。
type WorkspaceTrustPreviewResult struct {
	Root string `json:"root"` // 工作区目录
	// Key 信任范围：信任记录记在这个目录上（向上找到的 git 根，找不到就是工作区目录本身），其下所有子目录都继承。
	Key     string `json:"key"`
	Trusted bool   `json:"trusted"` // 当前运行时是否受信
	// Level 当前适用的信任记录（最近的上级记录）：trusted / untrusted；空 = 没有记录（界面里按不受信）。
	Level string `json:"level,omitempty"`
	Why   string `json:"why,omitempty"` // 当前信任结果的来由（trust.toml、命令行 -trust-project、无记录……）
	// Forced 启动参数强制受信（-trust-project / -project-hooks）：改信任记录对本次运行不起作用。
	Forced bool `json:"forced,omitempty"`
	// Files 受信后会被读取的项目配置文件与插件包目录（相对 root，/ 分隔）。
	Files      []string         `json:"files"`
	MCPServers []TrustMCPServer `json:"mcp_servers"`
	Hooks      []TrustHook      `json:"hooks"`
	// WidenedModes 项目 modes 目录里放宽了权限的档位。信任后首次选用（或作为缺省档位）仍要单独确认一次（approved = 已确认过同一放宽清单）。
	WidenedModes []TrustMode `json:"widened_modes"`
	// DefaultMode 项目 permissions.toml 设的新会话缺省档位；空 = 没设。
	DefaultMode string               `json:"default_mode,omitempty"`
	Packages    []TrustPackage       `json:"packages"` // 项目插件包
	Plugins     []TrustPluginSetting `json:"plugins"`  // 项目 profile.toml 的插件开关与配置
	// Notes 读取中的问题与提示（文件解析失败、负责的插件没开、按受信读配置会失败等），给人看。
	Notes []string `json:"notes,omitempty"`
}

// TrustMCPServer 受信后会启动的一个项目 MCP 服务。不回显 env / headers（可能带密钥）。
type TrustMCPServer struct {
	Name    string   `json:"name"`
	Command string   `json:"command,omitempty"` // stdio 服务的命令
	Args    []string `json:"args,omitempty"`
	URL     string   `json:"url,omitempty"` // http 服务的地址
	// Source project = 项目 mcp.toml；package:<名> = 项目插件包。
	Source string `json:"source"`
}

// TrustHook 受信后会执行的一条项目钩子。
type TrustHook struct {
	Event   string `json:"event"` // pre_tool_use / post_tool_use / stop
	Matcher string `json:"matcher,omitempty"`
	Command string `json:"command"`
	Source  string `json:"source"` // 同 TrustMCPServer.Source
}

// TrustMode 一个放宽了权限的项目档位。
type TrustMode struct {
	ID       string   `json:"id"`
	Title    string   `json:"title,omitempty"`
	Widened  []string `json:"widened"`
	Approved bool     `json:"approved,omitempty"`
}

// TrustPackage 一个项目插件包。
type TrustPackage struct {
	Name        string `json:"name"`
	Version     string `json:"version,omitempty"`
	Description string `json:"description,omitempty"`
	Dir         string `json:"dir"`
}

// TrustPluginSetting 项目 profile.toml 里的一行插件设置。
type TrustPluginSetting struct {
	Name    string   `json:"name"`
	Enabled *bool    `json:"enabled,omitempty"` // 没写 = 不改开关
	Keys    []string `json:"keys,omitempty"`    // 设了的配置键
	// Ignored 项目不能设、加载时会丢掉的键（端点、凭据、存储位置）。
	Ignored []string `json:"ignored,omitempty"`
}

// WorkspaceTrustArgs workspace.trust：写当前工作区的信任记录（范围 = 预览里的 key），然后重建运行时。
// Level：trusted 信任；untrusted 明确不信任；空串删除记录（撤销，按上级记录或不受信）。
// 有会话正在跑回合时同 workspace.reload：缺省 not_idle，Force 先取消再重建。
// 重建失败时信任记录改回原样：回到原来配置的返回 invalid（details.reverted = true）。
type WorkspaceTrustArgs struct {
	Level string `json:"level"`
	Force bool   `json:"force,omitempty"`
}

// WorkspaceTrustResult workspace.trust 的结果。Reloaded 为真时旧连接随后断开，客户端重连并重新 hello；
// 生效的信任状态没变（比如启动参数强制受信）时只写记录、不重建。
type WorkspaceTrustResult struct {
	Workspace WorkspaceInfo `json:"workspace"`
	Key       string        `json:"key"`
	Level     string        `json:"level"`
	Reloaded  bool          `json:"reloaded,omitempty"`
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
	Protocol string `json:"protocol,omitempty"` // openai-chat / anthropic-messages / 产品自定协议 ……
	BaseURL  string `json:"base_url,omitempty"`
	Name     string `json:"name,omitempty"`  // 厂商模型名
	Label    string `json:"label,omitempty"` // 界面显示名（同 ModelInfo.Label）
	// CredKind 凭据来源：env（环境变量）、none（不需要凭据）、other，或产品自定的种类。
	CredKind string `json:"cred_kind"`
	// CredLabel 给人看的凭据来源，如「环境变量 DEEPSEEK_API_KEY」「我的模型：DeepSeek 日常」「平台账号」。
	CredLabel string `json:"cred_label,omitempty"`
	// Origin 由哪项设置决定：flag（命令行参数）、env（环境变量）、profile（某层 profile）、auto（自动选择）、
	// builtin（内置缺省）；config = 配置给的但宿主没细分；"" = 应用直接指定。
	Origin string `json:"origin,omitempty"`
	// OriginRef 补充：参数名（-model）、变量名（<APP>_MODEL）、文件路径，或自动选择的理由。
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
	// Description 一句话说明（客户端改进 M10 T17）：内置档位是固定的中文说明，自定义档位取档位文件的 description；
	// 空 = 没写说明。
	Description string `json:"description,omitempty"`
}

// ModeListResult mode.list 的结果：可用档位清单。
type ModeListResult struct {
	Modes []ModeInfo `json:"modes"`
	// Default 新会话的缺省档位 id（客户端改进 M10）：permissions.toml 的 mode（或启动参数强制的档位）；
	// 它若是没确认过的放宽项目档位，这里给的是实际退回的内置档位。总是 modes 里的一项。
	// 空 = 服务端不知道（旧服务端，或档位实现没提供），客户端退回自己的假设。
	Default string `json:"default,omitempty"`
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
	Value   string `json:"value"` // switch / match：allow / ask / deny（match 型是未命中规则时的缺省）；level：级别
	Source  string `json:"source"`
	Bounded bool   `json:"bounded"`
	// 以下 docs/51 第 6 节（权限档位页）。Class 类别（read / write / exec / network / interact / spawn / collab /
	// orchestrate / external，即 toolhub.Effect*；MCP 服务按 trust 取 read / write / external）。
	Class string `json:"class,omitempty"`
	// Type switch（开关，三选一）/ match（匹配，另有规则）/ level（级别，取值在 Levels 里）。
	Type string `json:"type,omitempty"`
	// Owner 声明该项的插件（插件包声明的项也在这里出现）。
	Owner  string   `json:"owner,omitempty"`
	Tools  []string `json:"tools,omitempty"`  // 它管的工具名（按名字排序；还没有会话装配过时可能为空）
	Levels []string `json:"levels,omitempty"` // 仅 level 型
	// Allow / Ask / Deny 仅 match 型：规则（含 base 继承来的，继承链上是追加）。
	Allow []string `json:"allow,omitempty"`
	Ask   []string `json:"ask,omitempty"`
	Deny  []string `json:"deny,omitempty"`
	// Overridden 本档位文件自己写了这一项（否则沿 base / 建议值）。
	Overridden bool `json:"overridden,omitempty"`
}

// ModeShowResult mode.show 的结果：按权限项列出的取值与来源。
type ModeShowResult struct {
	Mode  string     `json:"mode"`
	Items []ModeItem `json:"items"`
	// 以下 docs/51 第 6 节：档位本身的信息。Editable 为真时可以 mode.save / mode.remove（用户级自定义档位）；
	// Revision 是档位文件当前版本，mode.save / remove 带上它防并发覆盖。
	Name        string `json:"name,omitempty"`
	Base        string `json:"base,omitempty"`
	Description string `json:"description,omitempty"`
	Builtin     bool   `json:"builtin,omitempty"`
	Project     bool   `json:"project,omitempty"`
	Editable    bool   `json:"editable,omitempty"`
	Revision    string `json:"revision,omitempty"`
}

// ModeItemDraft mode.save 里一个权限项的写法（同档位文件 [items] 的一项）：switch 型只给 default；
// match 型 default + allow / ask / deny 规则；level 型给 level。全空的项不写进文件（沿 base）。
type ModeItemDraft struct {
	Default string   `json:"default,omitempty"`
	Level   string   `json:"level,omitempty"`
	Allow   []string `json:"allow,omitempty"`
	Ask     []string `json:"ask,omitempty"`
	Deny    []string `json:"deny,omitempty"`
}

// ModeDraft 一份自定义档位。ID 即文件名（不能含空格和 /\:*?"<>|）；Base 继承的档位（通常是内置档位）；
// Items 键是权限项 id。规则沿 base 是追加的：base 已有的规则不必再写。
type ModeDraft struct {
	ID          string                   `json:"id"`
	Name        string                   `json:"name,omitempty"`
	Base        string                   `json:"base,omitempty"`
	Description string                   `json:"description,omitempty"`
	Items       map[string]ModeItemDraft `json:"items,omitempty"`
}

// ModeSaveArgs mode.save：新建或整份替换用户级自定义档位 <home>/modes/<id>.toml（格式同 mode copy）。
// 内置档位 id → invalid；项目档位 → invalid。Create 为真时同名档位已存在 → conflict（reason = exists）。
// Revision 非空且与文件当前版本不同 → conflict（reason = stale，details.revision 是当前版本）。
// 立即生效：用着这个档位的会话下一回合起按新定义判定。
type ModeSaveArgs struct {
	Mode     ModeDraft `json:"mode"`
	Revision string    `json:"revision,omitempty"`
	Create   bool      `json:"create,omitempty"`
}

// ModeSaveResult mode.save 的结果。
type ModeSaveResult struct {
	Mode     ModeInfo `json:"mode"`
	Revision string   `json:"revision"`
}

// ModeRemoveArgs mode.remove：删用户级自定义档位。内置 / 项目档位 → invalid；不存在 → not_found；
// 被别的档位当 base 时 → invalid（不删）。Revision 同 mode.save。
type ModeRemoveArgs struct {
	ID       string `json:"id"`
	Revision string `json:"revision,omitempty"`
}

// ModeRemoveResult mode.remove 的结果。FellBack 为真表示删的是新会话缺省档位，缺省已退回 Default
// （用户级 permissions.toml 的 mode 也改了），Note 是给人看的说明。已经在用这个档位的会话下一回合按「每次询问」处理。
type ModeRemoveResult struct {
	ID       string `json:"id"`
	Default  string `json:"default"`
	FellBack bool   `json:"fell_back,omitempty"`
	Note     string `json:"note,omitempty"`
}

// ModeSetDefaultArgs mode.setDefault：把用户级 permissions.toml 的 mode 设为 id（文件别的内容保留），
// 之后新建的会话用它；mode.list 的 default 随之变化。档位不存在 → not_found。
type ModeSetDefaultArgs struct {
	ID string `json:"id"`
}

// ModeSetDefaultResult mode.setDefault 的结果。Pinned 非空表示文件已写，但当前缺省档位被别处固定
// （启动参数强制、受信项目 permissions.toml 设了 mode），Default 仍是固定的那个。
type ModeSetDefaultResult struct {
	Default string `json:"default"`
	Pinned  string `json:"pinned,omitempty"`
}

// ArtifactGetArgs artifact.get：按 id 取一段工件内容。
type ArtifactGetArgs struct {
	Session string `json:"session"`
	Ref     string `json:"ref"`
	Offset  int    `json:"offset,omitempty"`
	Length  int    `json:"length,omitempty"`
	// AllWorkspaces 读别的工作区的会话（docs/63 W-1）：只对 operator 生效，缺省只读当前工作区。
	AllWorkspaces bool `json:"all_workspaces,omitempty"`
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
	// AllWorkspaces 读别的工作区的会话（docs/63 W-1）：只对 operator 生效，缺省只读当前工作区。
	AllWorkspaces bool `json:"all_workspaces,omitempty"`
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
	// AllWorkspaces 读别的工作区的会话（docs/63 W-1）：只对 operator 生效，缺省只读当前工作区。
	AllWorkspaces bool `json:"all_workspaces,omitempty"`
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

// 技能来源（SkillInfo.source；客户端改进 M11 配置T18）。插件包带的技能是 "package:<包名>"。
const (
	SkillSourceProject = "project"
	SkillSourceUser    = "user"
	SkillSourcePackage = "package"
)

// 技能启停范围（SkillsSetEnabledArgs.scope、SkillInfo.disabled_scope）。
const (
	SkillScopeGlobal    = "global"
	SkillScopeWorkspace = "workspace"
)

// SkillInfo 一个本地技能（skills.list，能力 skills）。
type SkillInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Source project（受信项目的 .<应用>/skills、.agents/skills）| user（用户级 <home>/skills）| package:<包名>。
	Source string `json:"source"`
	Path   string `json:"path"` // SKILL.md 或 <name>.md 的绝对路径
	// ModelInvocable false = 技能声明了 disable-model-invocation，启用也不进模型的技能目录。
	ModelInvocable bool `json:"model_invocable"`
	Enabled        bool `json:"enabled"`
	// DisabledScope 让它停用的记录范围（SkillScope*）；启用时为空。
	DisabledScope string `json:"disabled_scope,omitempty"`
	// DisabledReason 停用原因（给人看）；启用时为空。
	DisabledReason string `json:"disabled_reason,omitempty"`
}

// SkillsListResult skills.list 的结果：当前工作区看得到的全部技能（含已停用的），按名排序。
type SkillsListResult struct {
	Skills []SkillInfo `json:"skills"`
	// Workspace 工作区范围记录的键（与 hello 的 workspace 不一定同值：这里总是规范化的工作目录）；空 = 只能全局记录。
	Workspace string `json:"workspace,omitempty"`
	// Warnings 扫描时跳过的技能（格式不对、重名等）与启停记录读不出来的原因。
	Warnings []string `json:"warnings,omitempty"`
}

// SkillsSetEnabledArgs skills.setEnabled：启用或停用一个技能。记录写在用户级（不进项目仓库），对之后新建的会话生效。
type SkillsSetEnabledArgs struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	// Scope SkillScope*；空 = global。workspace 只影响当前工作区：与全局结果相同时删掉本工作区的覆盖。
	Scope string `json:"scope,omitempty"`
}

// SkillsSetEnabledResult skills.setEnabled 的结果：改完后这个技能在当前工作区的有效状态。
type SkillsSetEnabledResult struct {
	Skill SkillInfo `json:"skill"`
}

// SkillBodyMax skills.get 返回正文的上限（字节）；超出按 UTF-8 边界截断并置 truncated。
const SkillBodyMax = 64 << 10

// SkillsGetArgs skills.get：读一个技能的 SKILL.md（或 <name>.md）正文，含已停用的技能。
type SkillsGetArgs struct {
	Name string `json:"name"`
	// Workspace 可选：调用方以为的工作区键（skills.list 的 workspace）；给了且与当前不同返回 conflict。
	Workspace string `json:"workspace,omitempty"`
}

// SkillsGetResult skills.get 的结果。
type SkillsGetResult struct {
	Skill SkillInfo `json:"skill"`
	// Body 去掉 frontmatter 的正文（Markdown），最多 SkillBodyMax 字节。
	Body string `json:"body"`
	// Truncated 正文超过 SkillBodyMax 被截断。
	Truncated bool `json:"truncated,omitempty"`
	// Size 正文原始字节数（截断前）。
	Size int `json:"size"`
}

// MCP 服务的实例范围（MCPServerInfo.scope、MCPServerDraft.scope）与启用记录范围（MCPSetEnabledArgs.scope、
// MCPServerInfo.disabled_scope）：同一组值。
const (
	MCPScopeGlobal    = "global"
	MCPScopeWorkspace = "workspace"
)

// MCP 服务在本工作区的状态（MCPServerInfo.state）。
const (
	MCPStateConnected = "connected" // 运行中，tools 是它提供的工具
	MCPStateStarting  = "starting"  // 启用了但还没连上（或启用记录刚改、还没重建）
	MCPStateFailed    = "failed"    // 启动或 tools/list 失败，error 是原因
	MCPStateDisabled  = "disabled"  // 本工作区停用（disabled_scope 说明是哪条记录）
)

// MCP 服务的连接方式（MCPServerInfo.transport、MCPServerDraft.transport）。
const (
	MCPTransportStdio = "stdio"
	MCPTransportHTTP  = "http"
)

// MCPTool 服务提供的一个工具。
type MCPTool struct {
	Name        string `json:"name"`        // 服务端原名
	Public      string `json:"public"`      // 模型侧名字 mcp__<服务>__<工具>
	Description string `json:"description"` // 一句话（第一行，过长截断）
}

// MCPServerInfo 一个 MCP 服务定义与它在本工作区的状态（mcp.list，能力 mcp）。env / headers 只给键名，值不回显。
type MCPServerInfo struct {
	Name string `json:"name"`
	// Source user（用户级 mcp.toml）| project（受信项目的 mcp.toml）| package:<包名>（插件包）| config（-config 显式文件）。
	Source    string   `json:"source"`
	Transport string   `json:"transport"` // MCPTransport*
	Command   string   `json:"command,omitempty"`
	Args      []string `json:"args"`
	URL       string   `json:"url,omitempty"`
	// Cwd 定义里写的工作目录（用户级定义是文件原值，可直接回填编辑表单）；EffectiveCwd 实际使用的工作目录
	// （按工作区的 stdio 服务缺省为项目根，相对路径按项目根解析）。
	Cwd          string `json:"cwd,omitempty"`
	EffectiveCwd string `json:"effective_cwd,omitempty"`
	Timeout      int    `json:"timeout,omitempty"` // 秒；0 = 60
	Scope        string `json:"scope"`             // 生效的实例范围 MCPScope*
	Trust        string `json:"trust,omitempty"`   // ask（空）/ readonly / trusted
	Sandbox      string `json:"sandbox,omitempty"` // 空 = 缺省档位的沙箱
	Enabled      bool   `json:"enabled"`           // 本工作区的启用记录（最新记录）
	// DisabledScope 让它停用的那条记录的范围（MCPScope*）；启用时为空。
	DisabledScope string    `json:"disabled_scope,omitempty"`
	State         string    `json:"state"`           // MCPState*
	Error         string    `json:"error,omitempty"` // state = failed 时的原因
	Tools         []MCPTool `json:"tools"`           // 只有 connected 时非空
	EnvKeys       []string  `json:"env_keys"`
	HeaderKeys    []string  `json:"header_keys"`
	// Readonly 不能经 mcp.save / mcp.remove 改（插件包、-config 文件、项目定义：去对应文件改）。只能启停。
	Readonly bool `json:"readonly,omitempty"`
}

// MCPListResult mcp.list 的结果：本工作区看得到的全部服务（含停用的），顺序为用户级、项目、插件包。
type MCPListResult struct {
	Servers []MCPServerInfo `json:"servers"`
	// UserPath 用户级定义文件（mcp.save / remove 写这里）；ProjectPath 当前项目的定义文件（项目受信才生效）。
	UserPath    string `json:"user_path,omitempty"`
	ProjectPath string `json:"project_path,omitempty"`
	// Revision 用户级 mcp.toml 当前内容的版本号；mcp.save / remove 带上它做并发校验。
	Revision string `json:"revision"`
	// Editable mcp.save / remove 可用（应用支持写定义且没用 -config 显式文件）。
	Editable bool `json:"editable"`
	// Workspace 工作区范围启用记录的键（规范化的工作目录）；空 = 只能全局记录。
	Workspace string `json:"workspace,omitempty"`
	// Notes 给人看的说明（项目未受信时项目定义没列出、用了 -config 等）。
	Notes []string `json:"notes,omitempty"`
}

// MCPServerDraft 编辑表单里的一个服务定义（mcp.test、mcp.save）。command 与 url 须且只能给一个。
//
// env / headers 的写法（前端只知道键名，不知道值）：
//   - 键对应字符串：设成这个新值；
//   - 键对应 null：保留用户级 mcp.toml 里同名服务这个键的原值（原来没有这个键 → invalid）；
//   - 没提到的键：保留原值（同名服务原来有的键都留着）；
//   - 要删掉的键放进 env_remove / header_remove（与 env / headers 里同一个键同时出现 → invalid）。
//
// 所以「只改命令、不动密钥」时 env 可以整个不传。新建服务（用户级没有同名定义）时没有原值可保留。
type MCPServerDraft struct {
	Name      string   `json:"name"`
	Transport string   `json:"transport,omitempty"` // MCPTransport*；空 = 按 command / url 推断
	Command   string   `json:"command,omitempty"`
	Args      []string `json:"args,omitempty"`
	Cwd       string   `json:"cwd,omitempty"`
	URL       string   `json:"url,omitempty"`
	Timeout   int      `json:"timeout,omitempty"`
	Trust     string   `json:"trust,omitempty"`
	Sandbox   string   `json:"sandbox,omitempty"`
	Scope     string   `json:"scope,omitempty"` // MCPScope*；空 = workspace
	// Env / Headers 见类型注释：值为 null = 保留原值。
	Env          map[string]*string `json:"env,omitempty"`
	Headers      map[string]*string `json:"headers,omitempty"`
	EnvRemove    []string           `json:"env_remove,omitempty"`
	HeaderRemove []string           `json:"header_remove,omitempty"`
}

// MCPTestArgs mcp.test：用草稿定义临时连一次（会起进程或发请求，只给 operator），拉工具列表后断开；不保存、不注册工具。
type MCPTestArgs struct {
	Server MCPServerDraft `json:"server"`
}

// MCPTestResult mcp.test 的结果。连不上不报错：ok = false，error 是给人看的原因。
type MCPTestResult struct {
	OK    bool      `json:"ok"`
	Error string    `json:"error,omitempty"`
	Tools []MCPTool `json:"tools"`
}

// MCPSaveArgs mcp.save：在用户级 mcp.toml 里新增或替换同名 [[server]]（文件里别的段与注释原样保留，
// 被替换的那一段里的注释会丢），然后重建运行时（同 workspace.reload；全局范围、定义没变的服务进程不重启）。
// Revision 非空且与文件当前版本不同 → conflict（details.reason = stale，details.revision 是当前版本）。
// 插件包、项目、-config 来源的同名服务不能改 → invalid。有回合在跑时缺省 not_idle（什么都不写），Force 先取消再重建。
type MCPSaveArgs struct {
	Server   MCPServerDraft `json:"server"`
	Revision string         `json:"revision,omitempty"`
	Force    bool           `json:"force,omitempty"`
}

// MCPRemoveArgs mcp.remove：删掉用户级 mcp.toml 里的同名定义（没有 → not_found），并发校验与重建同 mcp.save。
type MCPRemoveArgs struct {
	Name     string `json:"name"`
	Revision string `json:"revision,omitempty"`
	Force    bool   `json:"force,omitempty"`
}

// MCPSetEnabledArgs mcp.setEnabled：写启用记录（用户级 mcp-enabled.toml，不进项目仓库）后重建运行时。
// scope = global 对所有工作区；workspace 只对当前工作区（与全局结果相同时删掉本工作区的覆盖）。空 = global。
type MCPSetEnabledArgs struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Scope   string `json:"scope,omitempty"`
	Force   bool   `json:"force,omitempty"`
}

// MCPEditResult mcp.save / remove / setEnabled 的结果。Reloaded 为真时旧连接随后断开，客户端重连、重新 hello 后
// 再调 mcp.list 看新状态；应用不能重建时（没有工作区管理器）Reloaded = false，改动在下次启动生效。
type MCPEditResult struct {
	Revision  string        `json:"revision"` // 写完后用户级 mcp.toml 的版本号
	Reloaded  bool          `json:"reloaded"`
	Workspace WorkspaceInfo `json:"workspace"`
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
	// Elem slice / array / map 的元素 Kind（map 是值的 Kind），如 string、struct、map（客户端改进 M10 T18）；
	// 其余类型为空。客户端遇到 type=slice 且 elem 不是标量（struct、map、slice、interface），或 type=map 时，
	// 只读显示当前值、不要把它放进 plugins.set 的 config（表单还不会编辑这类值，写回会把它写坏）。
	Elem string `json:"elem,omitempty"`
}

// PluginInfo 一个已装配组件的状态与配置。
type PluginInfo struct {
	Name string `json:"name"`
	// Version 组件版本（component.Meta.Version，客户端改进 M10 T17）；空 = 组件没写。
	Version string `json:"version,omitempty"`
	// Description 一句话说明（component.Meta.Description）；空 = 组件没写（如外部进程插件、应用自带的组件）。
	Description string        `json:"description,omitempty"`
	On          bool          `json:"on"`
	Default     bool          `json:"default"`
	Source      string        `json:"source"`
	Effects     []string      `json:"effects"`
	Schema      []ConfigField `json:"schema"`
	// Config 当前进程生效的配置值（profile 合并后，未写的键不出现）。
	Config map[string]any `json:"config"`
	// Mandatory 强制插件：不能停用。
	Mandatory bool `json:"mandatory,omitempty"`
	// Pending 改过、要重启才生效（按 plugins.set 写入的文件重新合并的结果与当前进程不同）。
	Pending *PluginPending `json:"pending,omitempty"`
	// Risks 打开这个插件前要确认的风险（客户端改进 M11 T24），由 effects 推断：exec → exec，network → network，
	// system_prompt / rewrite_result → prompt；tools 不算。前端在打开带 exec 或 network 的插件时弹确认。
	Risks []string `json:"risks,omitempty"`
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
	// Enabled 是否生效（客户端改进 M11 配置T22）。旧服务端不给这个字段：没有能力 packages.manage 时按启用处理。
	// 停用的用户级包也列出（enabled = false、contents 照常），不提供组件、不放界面。
	Enabled bool `json:"enabled"`
	// Dir 包目录（绝对路径）。
	Dir string `json:"dir,omitempty"`
	// Risks 风险项（Risk* 常量：exec / network / write / config / prompt / commands），推断规则见 runtime/pkgs/risks.go。
	Risks []string `json:"risks,omitempty"`
	// Managed 能经 packages.remove / setEnabled 管理（用户级包且应用支持 packages.manage）。项目级包只读。
	Managed bool `json:"managed,omitempty"`
	// Error 用户级目录里有这个包但没加载的原因（清单坏了、被同名项目包覆盖）；空 = 正常。
	Error string `json:"error,omitempty"`
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

// 插件包与插件的风险项（PackageInfo.risks、PackagesPreviewResult.risks、PluginInfo.risks）。
const (
	RiskExec     = "exec"     // 会在本机执行命令（stdio MCP 服务、钩子、exec 类插件）
	RiskNetwork  = "network"  // 会连外部地址（http MCP 服务、network 类插件）
	RiskWrite    = "write"    // 执行的命令不在只读沙箱里，可能改文件
	RiskConfig   = "config"   // 改内置插件的缺省配置
	RiskPrompt   = "prompt"   // 改模型看到的系统提示词或工具结果
	RiskCommands = "commands" // 界面按钮能调协议命令
)

// 预览时新包版本与已安装版本的比较（PackageInstalled.compare）。
const (
	VersionNewer   = "newer"   // 新包更新
	VersionSame    = "same"    // 相同
	VersionOlder   = "older"   // 新包更旧（降级）
	VersionUnknown = "unknown" // 有一方不是数字点分的版本号，比不出来
)

// PackagesPreviewArgs packages.preview：只读解析一个本地目录或 .zip（在服务端所在的机器上），不安装。
// 只给 operator（会读服务端本机任意路径）。
type PackagesPreviewArgs struct {
	Source string `json:"source"`
}

// PackagesPreviewResult 安装确认框要的内容。清单不合法时命令返回 invalid（message 说明原因）。
type PackagesPreviewResult struct {
	Name        string `json:"name"`
	Version     string `json:"version,omitempty"`
	Description string `json:"description,omitempty"`
	Contents    string `json:"contents"`
	// Dir 装好后的目录（<home>/plugins/<名>）；命令行与钩子里的 ${TENON_PLUGIN_DIR} 已按它展开。
	Dir        string           `json:"dir"`
	MCPServers []TrustMCPServer `json:"mcp_servers"` // source = package:<名>
	Hooks      []TrustHook      `json:"hooks"`
	Skills     []string         `json:"skills"`  // 技能名
	Prompts    []string         `json:"prompts"` // 提示词片段文件（相对包目录）
	Agents     []string         `json:"agents"`  // 定义（如 platform:helper）
	Config     []PackageConfig  `json:"config"`  // [config.<插件>] 的每个键
	UI         []UISlot         `json:"ui"`
	// UICommands 界面按钮 / 链接 / 表单能调的协议命令（视图里 kind = call 的 cmd）。
	UICommands []string `json:"ui_commands"`
	Risks      []string `json:"risks"`
	// Installed 已安装的同名用户级包；没有时不给。
	Installed *PackageInstalled `json:"installed,omitempty"`
	// Notes 提示：负责的插件没开、会被忽略的配置键、本地包没有签名等。
	Notes []string `json:"notes,omitempty"`
}

// PackageConfig 插件包给某个内置插件设的一项缺省配置。
type PackageConfig struct {
	Plugin string `json:"plugin"`
	Key    string `json:"key"`
	Value  any    `json:"value"`
	// Ignored 插件包不能设（端点、凭据、存储位置），加载时会丢掉。
	Ignored bool `json:"ignored,omitempty"`
}

// PackageInstalled 预览时已安装的同名包。
type PackageInstalled struct {
	Version string `json:"version,omitempty"`
	Enabled bool   `json:"enabled"`
	Compare string `json:"compare"`         // Version*：新包相对已安装的
	Error   string `json:"error,omitempty"` // 已安装的那份清单坏了
}

// PackagesInstallArgs packages.install：把 source 装到用户级插件包目录。同名已安装时旧包挪进
// <home>/plugins/.trash/<名>-<时间>（可手工挪回恢复）。force 同 mcp.save：有回合在跑时先取消再重建。
type PackagesInstallArgs struct {
	Source string `json:"source"`
	Force  bool   `json:"force,omitempty"`
}

// PackagesRemoveArgs packages.remove：把用户级包挪进 .trash（不删除）。项目包 → invalid；没有 → not_found。
type PackagesRemoveArgs struct {
	Name  string `json:"name"`
	Force bool   `json:"force,omitempty"`
}

// PackagesSetEnabledArgs packages.setEnabled：启停用户级包（记录在 <home>/plugins-enabled.toml）。
type PackagesSetEnabledArgs struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Force   bool   `json:"force,omitempty"`
}

// PackagesEditResult packages.install / remove / setEnabled 的结果。Reloaded 为真时旧连接随后断开，
// 客户端重连、重新 hello 后再调 packages.list；setEnabled 状态没变时不重建（reloaded = false）。
type PackagesEditResult struct {
	Name string `json:"name"`
	// Trashed 挪进 .trash 的旧包目录（覆盖安装、卸载）；空 = 没有。
	Trashed   string        `json:"trashed,omitempty"`
	Reloaded  bool          `json:"reloaded"`
	Workspace WorkspaceInfo `json:"workspace"`
}
