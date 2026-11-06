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
}

type PluginsListResult struct {
	Plugins []PluginInfo `json:"plugins"`
}

type PluginsSetArgs struct {
	Name    string         `json:"name"`
	Enabled *bool          `json:"enabled,omitempty"`
	Config  map[string]any `json:"config,omitempty"`
	Scope   string         `json:"scope"` // user / project
}

type PackageInfo struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	Scope       string `json:"scope"` // user / project
	Source      string `json:"source"`
	Contents    string `json:"contents"`
}

type PackagesListResult struct {
	Packages []PackageInfo `json:"packages"`
}
