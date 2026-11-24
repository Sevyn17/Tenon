package appapi

// 应用协议命令（protocol 2，docs/01 - 设计文档/06 - 应用协议契约.md 10.4）：参数与结果全部用 appapi/wire 的线上类型，错误码按 10.7。
// 入口是 App.Call2（进程内直接调用用 App.V2() 的方法）；订阅（session.subscribe / unsubscribe）与按连接身份回答交互
// 在 Conn 上（conn.go）。protocol 1 已在 10.11 S8 删除。
//
// 约定（10.4）：任何命令都不隐式新建会话（G1）；改日志的命令接受 if_cursor；send / steer 必带 client_id。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/runtime/agentdef"
	"github.com/Sevyn17/tenon/runtime/appid"
	"github.com/Sevyn17/tenon/runtime/bus"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/host"
	"github.com/Sevyn17/tenon/runtime/reqlog"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
	"github.com/Sevyn17/tenon/runtime/toolhub"
	"github.com/Sevyn17/tenon/runtime/workmode"
)

// LocalPrincipal 桌面端与单机网页服务的连接身份（10.8）。
var LocalPrincipal = wire.Principal{ID: "local", Role: wire.RoleOperator}

// 工件命令的上限：单次 artifact.get 最多返回的字节数、artifact.put 最多收的字节数（base64 后都在 4 MiB 帧上限以内）。
const (
	MaxArtifactChunk = 1 << 20
	MaxArtifactPut   = 2 << 20
)

// applyWait SetExt / SetConfig 最多同步等多久（见 App.apply）。
var applyWait = 2 * time.Second

// metaClientID session.create 记在会话行业务键里的 client_id（同一 client_id 重发返回已建会话）。
const metaClientID = "client_id"

type handler func(ctx context.Context, a *App, args json.RawMessage) (any, error)

// bind 同 bind，错误按 protocol 2 归类（AsError）。
func bind[In, Out any](f func(*App, context.Context, In) (Out, error)) handler {
	return func(ctx context.Context, a *App, raw json.RawMessage) (any, error) {
		var in In
		if t := bytes.TrimSpace(raw); len(t) > 0 && !bytes.Equal(t, []byte("null")) {
			if err := json.Unmarshal(t, &in); err != nil {
				return nil, Errorf(wire.CodeBadRequest, "invalid arguments: %v", err)
			}
		}
		out, err := f(a, ctx, in)
		if err != nil {
			return nil, AsError(err)
		}
		return out, nil
	}
}

// commands 核心命令表：各模块文件在自己的 init 里用 registerCommands 登记（docs/61 T0：按文件登记，新命令写在 appapi/<mod>.go）。
var commands = map[string]handler{}

// registerCommands 登记核心命令；同名重复登记 panic（启动即失败）。
func registerCommands(m map[string]handler) {
	for k, h := range m {
		if _, dup := commands[k]; dup {
			panic("appapi: command registered twice: " + k)
		}
		commands[k] = h
	}
}

// 握手、会话、档位、工件、插件、工作区等基础命令（实现在本文件）。其余模块的命令在各自文件的 init 里登记：
// interact.go、queue.go、mode_edit.go、packages.go、changes.go、skills.go、mcp.go、trust.go。
func init() {
	registerCommands(map[string]handler{
		"hello":             bind((*App).Hello),
		"sessions.list":     bind((*App).SessionsList),
		"session.create":    bind((*App).Create),
		"session.update":    bind((*App).Update),
		"session.delete":    bind((*App).Delete),
		"feedback.list":     bind((*App).FeedbackList),
		"session.followups": bind((*App).SessionFollowups),
		"feedback.set": bind(func(v *App, ctx context.Context, in wire.FeedbackArgs) (wire.Feedback, error) {
			return v.FeedbackSet(ctx, in, LocalPrincipal.ID) // 进程内调用按本机身份；连接上按连接身份（conn.go）
		}),
		"session.get":      bind((*App).Get),
		"session.entries":  bind((*App).Entries),
		"session.send":     bind((*App).Send),
		"session.steer":    bind((*App).Steer),
		"session.cancel":   bind((*App).Cancel),
		"session.rewind":   bind((*App).Rewind),
		"session.compact":  bind((*App).Compact),
		"session.fork":     bind((*App).Fork),
		"session.stats":    bind((*App).Stats),
		"session.settings": bind((*App).Settings),
		"mode.list":        bind((*App).ModeList),
		"mode.get":         bind((*App).ModeGet),
		"mode.set":         bind((*App).ModeSet),
		"mode.show":        bind((*App).ModeShow),
		"artifact.get":     bind((*App).ArtifactGet),
		"artifact.put":     bind((*App).ArtifactPut),
		"trace.explain":    bind((*App).Trace),
		"plugins.list":     bind((*App).PluginsList),
		"plugins.set":      bind((*App).PluginsSet),
		"workspace.files":  bind((*App).WorkspaceFiles),
		"models.list":      bind((*App).ModelsList),
		"workspace.list":   bind((*App).WorkspaceList),
		"session.locate":   bind((*App).Locate),
		"workspace.open":   bind((*App).WorkspaceOpen),
		"workspace.reload": bind((*App).WorkspaceReload),
	})
}

// Commands protocol 2 支持的命令名（排序后）。
func Commands() []string {
	out := make([]string, 0, len(commands))
	for k := range commands {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Call 按命令名分发 protocol 2 命令。错误一律是 *Error（码见 wire.Code*）。
func (a *App) Call(ctx context.Context, cmd string, args json.RawMessage) (any, error) {
	h, ok := commands[cmd]
	if !ok {
		if out, ok, err := a.callExt(ctx, LocalPrincipal, cmd, args); ok { // 产品扩展命令（ext.go），进程内按本机身份
			return out, err
		}
		return nil, Errorf(wire.CodeUnsupported, "unknown command %q", cmd)
	}
	return h(ctx, a, args)
}

// AsError 按错误码表（10.7）归类。已是 *Error 的：带原错误的按原错误重新归类（结果不变，多包几层也一样），
// 不带的原样返回。
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		if e.err == nil {
			return e
		}
		err = e.err
	}
	out := &Error{Code: wire.CodeInternal, Message: err.Error(), err: err}
	reason := ""
	switch {
	case errors.Is(err, host.ErrBusy):
		out.Code, out.Retry = wire.CodeBusy, true
	case errors.Is(err, host.ErrStale):
		out.Code, reason = wire.CodeConflict, wire.ConflictStale
	case errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrLeaseLost):
		out.Code, reason = wire.CodeConflict, wire.ConflictLeased
	case errors.Is(err, store.ErrCursorExpired):
		out.Code = wire.CodeCursorExpired
	case errors.Is(err, host.ErrClosed):
		out.Code = wire.CodeClosed
	case errors.Is(err, store.ErrNotFound), errors.Is(err, host.ErrSessionNotFound), errors.Is(err, agentdef.ErrNotFound):
		out.Code = wire.CodeNotFound
	case errors.Is(err, host.ErrNotAllowed):
		out.Code = wire.CodeDenied
	case errors.Is(err, host.ErrOtherWorkspace): // 在别的工作区里打开它：工具会在错的目录里执行
		out.Code = wire.CodeInvalid
	case errors.Is(err, session.ErrNotIdle):
		out.Code = wire.CodeNotIdle
	case errors.Is(err, session.ErrNotWaiting): // steer 时没有进行中的回合：提示用户直接发送，不是"等一会再试"
		out.Code = wire.CodeInvalid
	case errors.Is(err, session.ErrBadRewind), errors.Is(err, session.ErrReadOnly),
		errors.Is(err, session.ErrExtTooLarge), errors.Is(err, session.ErrToolsOpen):
		out.Code = wire.CodeInvalid
	case errors.Is(err, session.ErrState):
		out.Code = wire.CodeNotIdle
	}
	if reason != "" {
		out.Details = map[string]any{"reason": reason}
	}
	return out
}

// conflict 带 details.reason 的 conflict 错误。
func conflict(reason, format string, args ...any) *Error {
	e := Errorf(wire.CodeConflict, format, args...)
	e.Details = map[string]any{"reason": reason}
	return e
}

func invalid(format string, args ...any) *Error { return Errorf(wire.CodeInvalid, format, args...) }

// —— 握手与会话 ——

// Hello 握手：客户端报支持的协议区间（都缺省 = 只认当前版本），不含 protocol 2 返回 unsupported。
func (v *App) Hello(_ context.Context, in wire.HelloArgs) (wire.HelloResult, error) {
	lo, hi := in.Protocol.Min, in.Protocol.Max
	if lo == 0 && hi == 0 {
		lo, hi = wire.Protocol, wire.Protocol
	}
	if lo > wire.Protocol || hi < wire.Protocol {
		return wire.HelloResult{}, Errorf(wire.CodeUnsupported, "protocol range [%d, %d] does not include server protocol %d", lo, hi, wire.Protocol)
	}
	o := v.o
	ws := "" // 当前工作区的键（客户端改进 M7）：与 SessionInfo.workspace 同值，客户端据此判断 sessions.changed 的行属不属于缺省列表
	if o.Host != nil {
		ws = o.Host.Workspace
	}
	return wire.HelloResult{Protocol: wire.Protocol, Server: appid.Name(), Principal: LocalPrincipal,
		Capabilities: v.capabilities(), Model: o.Model, Workdir: o.Workdir, Trusted: o.Trusted, MaxOutput: MaxWireText,
		Workspace: ws, WorkspaceID: wire.WorkspaceID(ws), Extensions: v.extInfos()}, nil
}

// capabilities 按装配了哪些部件列出可选功能，界面据此决定显示哪些入口。
func (v *App) capabilities() []string {
	o := v.o
	out := []string{}
	if o.Artifacts != nil {
		out = append(out, "artifacts")
	}
	if o.Compactor != nil {
		out = append(out, "compact")
	}
	if o.Modes != nil {
		out = append(out, "modes")
		if ed, ok := o.Modes.(ModeEditor); ok && ed.Editable() { // docs/51 第 6 节：界面里编辑档位
			out = append(out, CapModeEdit)
		}
	}
	if o.Profile != nil {
		out = append(out, "plugins")
	}
	if o.Files != nil {
		out = append(out, "files")
	}
	if o.Models != nil {
		out = append(out, "models")
	}
	if o.Workspaces != nil {
		out = append(out, "workspaces")
		// reload（客户端改进 M3）：工作区管理器能在当前目录上重建运行时（workspace.reload）。CLI 没有管理器，不报。
		if _, ok := o.Workspaces.(Reloader); ok {
			out = append(out, "reload")
			if _, ok := o.Workspaces.(Truster); ok { // 客户端改进 M11：界面里信任项目（trust.go）
				out = append(out, CapWorkspaceTrust)
			}
			if _, ok := o.Workspaces.(PackageManager); ok { // 客户端改进 M11 配置T22：插件包管理（packages.go）
				out = append(out, CapPackagesManage)
			}
		}
	}
	// 平台账号与按厂商管理的自定义模型（platform、providers 等）由产品扩展经 Extension.Capabilities 报（docs/61 T1、T2）。
	if o.Followups != nil {
		out = append(out, "followups")
	}
	if o.Upload && o.Artifacts != nil {
		out = append(out, "upload")
	}
	// last_end（客户端改进 M2）：快照与 session.state_changed 带 last_end、turn.ended 带 at、
	// 失败时 code 只放错误码。总是有；旧服务端没有这条，客户端据此退回原逻辑。
	out = append(out, "last_end")
	// sessions_unassigned（客户端改进 M6 决定 10）：sessions.list {unassigned} 可用。宿主有工作区
	// （缺省列表按工作区过滤，没有工作区的旧会话看不到）且存储实现了 UnassignedLister 才报。
	if _, ok := o.DB.(store.UnassignedLister); ok && o.Host != nil && o.Host.Workspace != "" {
		out = append(out, "sessions_unassigned")
	}
	// sessions.query（客户端改进 M7 协议T13）：sessions.list {query, parent, archived} 与 session.update {archived}。
	// 存储实现了 SessionQueryStore（按条件列）和 SessionMetaStore（写归档标记）才报。
	if _, ok := o.DB.(store.SessionQueryStore); ok {
		if _, ok := o.DB.(store.SessionMetaStore); ok {
			out = append(out, "sessions.query")
		}
	}
	// sessions.changed.full（客户端改进 M7 协议T10）：每条非 deleted 的 sessions.changed 都带齐 preview、parent、origin、
	// workspace、archived、last_end 的当前值（空值不发 = 空）。总是有；旧服务端没有，客户端不能把「没给」当成空。
	out = append(out, "sessions.changed.full")
	// entries.turn（客户端改进 M8 协议T15）：session.entries {turn} 与 session.fork {upto_turn}。总是有
	// （存储没实现 TurnRangeStore 时扫条目兜底）；旧服务端会忽略这两个参数，客户端没有这条能力时必须停用相关动作。
	out = append(out, "entries.turn")
	// model_retrying（客户端改进 M8）：模型每次退避重试前发易失事件 model.retrying。
	out = append(out, "model_retrying")
	// notices（客户端改进 M10 T19）：notice 连接级送达、带 level / source，第一次 hello 后补发最近 20 条。总是有。
	out = append(out, CapNotices)
	// send.queue（客户端改进 M12 协议T20）：session.send {queue}、session.unqueue、快照 queued 与 queue.changed。
	// 存储实现了 QueueStore 才报；没有时 queue 被忽略（按原来的插入处理），session.unqueue 返回 unsupported。
	if v.queueOK() {
		out = append(out, CapSendQueue)
	}
	// changes（客户端改进 M9）：changes.list / diff / revert 与 session.rewind {restore_files, force}。装了 checkpoint 才报。
	if o.Changes != nil {
		out = append(out, "changes")
	}
	// changes.git（M9 T10）：changes.list / diff 接受 scope=git（未提交改动，只读）。本机有 git 且工作区在仓库里才报。
	if v.gitScope() {
		out = append(out, "changes.git")
	}
	// skills（M11 配置T18）：skills.list / skills.setEnabled。开了 skills 插件才报。
	if o.Skills != nil {
		out = append(out, "skills")
	}
	// mcp（M11 配置T14）：mcp.list / test / save / remove / setEnabled。开了 mcp 插件才报（save / remove 是否可用看 editable）。
	if o.MCP != nil {
		out = append(out, CapMCP)
	}
	// work_mode（M12 Plan 模式）：session.settings {work_mode}、Settings.work_mode、plan 交互。随 plan-mode 插件报
	// （它提供 exit_plan；关掉后界面不给计划入口，docs/52 R3、对齐报告 P2-1）；没装配 Profile 时照旧总是报。
	planOn := o.Profile == nil
	if o.Profile != nil {
		for _, st := range o.Profile.States() {
			planOn = planOn || (st.Name == "plan-mode" && st.On)
		}
	}
	if planOn {
		out = append(out, CapWorkMode)
	}
	// 产品扩展的能力（docs/61 3.2，ext.go）：照常并进来，不重复。
	if v.ext != nil {
		for _, c := range v.ext.caps {
			if !slices.Contains(out, c) {
				out = append(out, c)
			}
		}
	}
	return out
}

// SessionsList 按更新时间倒序分页列会话。
func (v *App) SessionsList(ctx context.Context, in wire.ListArgs) (wire.ListResult, error) {
	limit := in.Limit
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	ws := v.o.Host.Workspace // 缺省只列当前工作区的会话
	if in.AllWorkspaces {
		ws = ""
	}
	// query / parent / archived（客户端改进 M7 协议T13）：要存储实现 SessionQueryStore（能力 sessions.query）。
	archived, err := archivedFilter(in.Archived)
	if err != nil {
		return wire.ListResult{}, err
	}
	query := strings.TrimSpace(in.Query)
	if len(query) > MaxQuery {
		return wire.ListResult{}, invalid("query exceeds %d bytes", MaxQuery)
	}
	qs, canQuery := v.o.DB.(store.SessionQueryStore)
	if (query != "" || in.Parent != "" || in.Archived != "" || in.Agent != "") && !canQuery {
		return wire.ListResult{}, Errorf(wire.CodeUnsupported, "store does not support search, parent, archive or agent filters")
	}
	if in.Pinned && (query != "" || in.Parent != "") {
		return wire.ListResult{}, invalid("pinned cannot be combined with query or parent")
	}
	if in.Parent != "" && in.Unassigned {
		return wire.ListResult{}, invalid("parent cannot be combined with unassigned")
	}
	// unassigned：只列没有工作区的会话（客户端改进 M6 决定 10，左栏「未归属」分组）。
	var ul store.UnassignedLister
	if in.Unassigned {
		var ok bool
		if ul, ok = v.o.DB.(store.UnassignedLister); !ok {
			return wire.ListResult{}, Errorf(wire.CodeUnsupported, "store does not support listing sessions without a workspace")
		}
	}
	inWS := func(s store.SessionInfo) bool {
		if in.Unassigned {
			return s.Workspace == ""
		}
		return ws == "" || s.Workspace == ws
	}
	if in.Pinned { // 置顶的会话：单独一页，不分页
		ms, ok := v.o.DB.(store.SessionMetaStore)
		if !ok {
			return wire.ListResult{Items: []wire.SessionInfo{}}, nil
		}
		items, err := ms.SessionsWithMeta(ctx, metaPinned, limit)
		if err != nil {
			return wire.ListResult{}, err
		}
		out := wire.ListResult{Items: []wire.SessionInfo{}}
		for _, s := range items {
			if (in.All || store.TopLevel(s.ParentID, s.Origin)) && inWS(s) && keepArchived(archived, s.Meta) &&
				(in.Agent == "" || s.AgentID == in.Agent) {
				out.Items = append(out.Items, wireInfo(s))
			}
		}
		return out, nil
	}
	var items []store.SessionInfo
	switch {
	case canQuery:
		q := store.SessionQuery{Workspace: ws, Unassigned: in.Unassigned, Before: in.Cursor, Limit: limit, TopOnly: !in.All,
			Parent: in.Parent, Text: query, Archived: archived, Agent: in.Agent}
		if in.Parent != "" { // 子会话跟随父会话的工作区，按 parent 列时不再按工作区、顶层过滤
			q.Workspace, q.TopOnly = "", false
		}
		items, err = qs.QuerySessions(ctx, q)
	case in.Unassigned:
		items, err = ul.ListUnassigned(ctx, in.Cursor, limit, !in.All)
	default:
		items, err = v.o.DB.ListSessionsIn(ctx, ws, in.Cursor, limit, !in.All)
	}
	if err != nil {
		return wire.ListResult{}, err
	}
	out := wire.ListResult{Items: make([]wire.SessionInfo, len(items))}
	if len(items) == limit {
		out.Next = items[len(items)-1].UpdatedAt
	}
	for i, s := range items {
		out.Items[i] = wireInfo(s)
	}
	return out, nil
}

// metaPinned 会话行业务键里的置顶标记（session.update {pinned}）。
const metaPinned = "pinned"

// pinnedOf 业务键里是否置顶。
func pinnedOf(meta json.RawMessage) bool {
	var m map[string]any
	_ = json.Unmarshal(meta, &m)
	return m[metaPinned] == true
}

// sourceOf 业务键里的来源与引用（session.create {source, ref}）。
func sourceOf(meta json.RawMessage) (source, ref string) {
	var m map[string]any
	_ = json.Unmarshal(meta, &m)
	source, _ = m[metaSource].(string)
	ref, _ = m[metaRef].(string)
	return source, ref
}

func wireInfo(s store.SessionInfo) wire.SessionInfo {
	src, ref := sourceOf(s.Meta)
	return wire.SessionInfo{ID: s.ID, Title: s.Title, Preview: s.Preview, RunState: wireRun(s.RunState),
		Origin: s.Origin, Parent: s.ParentID, UpdatedAt: s.UpdatedAt, Pinned: pinnedOf(s.Meta), Workspace: s.Workspace, Agent: s.AgentID,
		Archived: store.ArchivedOf(s.Meta), LastEnd: WireLastEnd(s.LastEnd), Source: src, Ref: ref}
}

// MaxQuery sessions.list {query} 的上限（字节）。用户消息只比较前 200 字，再长也搜不到。
const MaxQuery = 1000

// archivedFilter sessions.list {archived} → 存储的筛选；不认识的取值返回 invalid。
func archivedFilter(s string) (store.ArchivedFilter, error) {
	switch s {
	case "":
		return store.ArchivedExclude, nil
	case wire.ArchivedOnly:
		return store.ArchivedOnly, nil
	case wire.ArchivedInclude:
		return store.ArchivedInclude, nil
	}
	return 0, invalid("archived must be empty, %q or %q", wire.ArchivedOnly, wire.ArchivedInclude)
}

// keepArchived 按筛选决定要不要这一行（置顶列表在这里筛，它不走 QuerySessions）。
func keepArchived(f store.ArchivedFilter, meta json.RawMessage) bool {
	a := store.ArchivedOf(meta)
	return f == store.ArchivedInclude || (f == store.ArchivedOnly) == a
}

// unarchive 往已归档的会话发消息时自动恢复（spike 第 4 节待拍板的推荐）。在输入已投递之后做：投递失败不改归档状态；
// 恢复失败不影响已投递的输入（会话留在已归档里，用户可以手动恢复）。
func (v *App) unarchive(ctx context.Context, id string) {
	ms, ok := v.o.DB.(store.SessionMetaStore)
	if !ok {
		return
	}
	l, err := host.LoadHead(ctx, v.o.DB, id)
	if err != nil || !store.ArchivedOf(l.Meta.Meta) {
		return
	}
	_ = ms.UpdateSessionMeta(ctx, id, nil, map[string]any{store.MetaArchived: nil})
}

// MaxTitle 会话标题上限（字节）。
const MaxTitle = 200

// Update 改会话标题、置顶（会话行，不碰日志，回合中也能改）。落库后发 session.updated（持久）与 sessions.changed。
func (v *App) Update(ctx context.Context, in wire.UpdateArgs) (wire.SessionSnapshot, error) {
	if err := need(in.Session); err != nil {
		return wire.SessionSnapshot{}, err
	}
	ms, ok := v.o.DB.(store.SessionMetaStore)
	if !ok {
		return wire.SessionSnapshot{}, Errorf(wire.CodeUnsupported, "store does not support updating session metadata")
	}
	var title *string
	if in.Title != nil {
		t := strings.TrimSpace(*in.Title)
		if len(t) > MaxTitle {
			return wire.SessionSnapshot{}, invalid("title exceeds %d bytes", MaxTitle)
		}
		for _, r := range t {
			if r < 0x20 || r == 0x7f {
				return wire.SessionSnapshot{}, invalid("title must not contain newlines or control characters")
			}
		}
		title = &t
	}
	set := map[string]any{}
	if in.Pinned != nil {
		if *in.Pinned {
			set[metaPinned] = true
		} else {
			set[metaPinned] = nil
		}
	}
	// 归档（客户端改进 M7 协议T13）：要能力 sessions.query（存储能把已归档的从缺省列表里排除，才允许归档）。
	if in.Archived != nil {
		if _, ok := v.o.DB.(store.SessionQueryStore); !ok {
			return wire.SessionSnapshot{}, Errorf(wire.CodeUnsupported, "store does not support archiving")
		}
		if *in.Archived {
			if in.Pinned != nil && *in.Pinned {
				return wire.SessionSnapshot{}, invalid("archived:true cannot be combined with pinned:true")
			}
			set[store.MetaArchived] = true
			set[metaPinned] = nil // 归档同时取消置顶（spike 第 4 节待拍板的推荐）
		} else {
			set[store.MetaArchived] = nil
		}
	} else if in.Pinned != nil && *in.Pinned {
		// 给已归档的会话置顶：置顶列表排除已归档的，置了也看不到；要先恢复（同一请求带 archived:false 即可）。
		l, err := host.LoadHead(ctx, v.o.DB, in.Session)
		if err != nil {
			return wire.SessionSnapshot{}, err
		}
		if store.ArchivedOf(l.Meta.Meta) {
			return wire.SessionSnapshot{}, conflict("archived", "session is archived; restore it before pinning")
		}
	}
	if title == nil && len(set) == 0 {
		return v.snapshot(ctx, in.Session)
	}
	if err := ms.UpdateSessionMeta(ctx, in.Session, title, set); err != nil {
		return wire.SessionSnapshot{}, err
	}
	return v.snapshot(ctx, in.Session)
}

// followupCache 建议追问的缓存：（会话, 回合, 日志长度）→ 结果（同一回合多端、刷新都不重复调模型）。只留最近一批。
type followupCache struct {
	mu sync.Mutex
	m  map[string][]string
}

func (c *followupCache) get(k string) ([]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[k]
	return v, ok
}

func (c *followupCache) put(k string, v []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil || len(c.m) > 256 {
		c.m = map[string][]string{}
	}
	c.m[k] = v
}

// SessionFollowups 某回合之后的建议追问：取这一回合的用户输入与最后一条助手正文，交给模型侧调用生成；
// 回合还没结束或没有正文时返回空。结果按（会话, 回合, 日志长度）缓存。
func (v *App) SessionFollowups(ctx context.Context, in wire.FollowupsArgs) (wire.FollowupsResult, error) {
	if v.o.Followups == nil {
		return wire.FollowupsResult{}, Errorf(wire.CodeUnsupported, "follow-up suggestions not available")
	}
	if err := need(in.Session); err != nil {
		return wire.FollowupsResult{}, err
	}
	l, err := host.LoadHead(ctx, v.o.DB, in.Session)
	if err != nil {
		return wire.FollowupsResult{}, err
	}
	h := l.Snapshot.Header
	from := max(h.LogLen-200, h.CompactionBase)
	es, err := v.o.DB.Entries(ctx, in.Session, from, h.LogLen)
	if err != nil {
		return wire.FollowupsResult{}, err
	}
	out := wire.FollowupsResult{Turn: in.Turn, Items: []string{}}
	if len(es) == 0 {
		return out, nil
	}
	last := es[len(es)-1].Turn
	if out.Turn == 0 {
		out.Turn = last
	}
	if h.Status != session.StatusIdle && out.Turn == last {
		return out, nil // 回合还在进行
	}
	key := fmt.Sprintf("%s|%d|%d", in.Session, out.Turn, h.LogLen)
	if got, ok := v.followups.get(key); ok {
		out.Items = got
		return out, nil
	}
	var input, answer string
	for _, e := range es {
		if e.Turn != out.Turn {
			continue
		}
		switch {
		case e.Item.Kind == session.KindUser && e.Item.User != nil && input == "":
			input = textOf(e.Item.User.Content)
		case e.Item.Kind == session.KindAssistant && e.Item.Assistant != nil:
			if t := textOf(e.Item.Assistant.Content); strings.TrimSpace(t) != "" {
				answer = t
			}
		}
	}
	if strings.TrimSpace(answer) == "" {
		return out, nil
	}
	model := ""
	if h.Config != nil {
		model = h.Config.Model
	}
	items, err := v.o.Followups(ctx, model, clip(input, 4000), clip(answer, 8000))
	if err != nil {
		return wire.FollowupsResult{}, err
	}
	if items == nil {
		items = []string{}
	}
	if len(items) > 0 { // 空结果不缓存（侧调用失败也是空），下次再试
		v.followups.put(key, items)
	}
	out.Items = items
	return out, nil
}

func textOf(c session.Content) string {
	var sb strings.Builder
	for _, p := range c {
		if p.Kind == "text" {
			sb.WriteString(p.Text)
		}
	}
	return sb.String()
}

// clip 按字节截断（不切坏 UTF-8），保留开头。
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// metaFeedback 会话行业务键里的反馈：条目 id → Feedback。
const metaFeedback = "feedback"

// MaxFeedbackNote 反馈附言上限（字节）。
const MaxFeedbackNote = 2000

// FeedbackSet 给 assistant 条目打分（存会话行业务键，随会话删除；回合中也能改）。by 是连接身份。
func (v *App) FeedbackSet(ctx context.Context, in wire.FeedbackArgs, by string) (wire.Feedback, error) {
	if err := need(in.Session); err != nil {
		return wire.Feedback{}, err
	}
	ms, ok := v.o.DB.(store.SessionMetaStore)
	if !ok {
		return wire.Feedback{}, Errorf(wire.CodeUnsupported, "store does not support session metadata")
	}
	if in.Entry == "" || len(in.Entry) > 128 {
		return wire.Feedback{}, Errorf(wire.CodeBadRequest, "entry missing or exceeds 128 bytes")
	}
	if in.Vote != "" && in.Vote != "up" && in.Vote != "down" {
		return wire.Feedback{}, invalid("vote must be up, down or empty, got %q", in.Vote)
	}
	if len(in.Note) > MaxFeedbackNote {
		return wire.Feedback{}, invalid("note exceeds %d bytes", MaxFeedbackNote)
	}
	l, err := host.LoadHead(ctx, v.o.DB, in.Session)
	if err != nil {
		return wire.Feedback{}, err
	}
	all := feedbackOf(l.Meta.Meta)
	fb := wire.Feedback{Entry: in.Entry, Vote: in.Vote, Note: in.Note, By: by, At: time.Now().UnixMilli()}
	if in.Vote == "" {
		delete(all, in.Entry)
	} else {
		all[in.Entry] = fb
	}
	var val any = all
	if len(all) == 0 {
		val = nil
	}
	if err := ms.UpdateSessionMeta(ctx, in.Session, nil, map[string]any{metaFeedback: val}); err != nil {
		return wire.Feedback{}, err
	}
	return fb, nil
}

// FeedbackList 一个会话的全部反馈（按时间）。
func (v *App) FeedbackList(ctx context.Context, in wire.SessionArgs) (wire.FeedbackListResult, error) {
	if err := need(in.Session); err != nil {
		return wire.FeedbackListResult{}, err
	}
	l, err := host.LoadHead(ctx, v.o.DB, in.Session)
	if err != nil {
		return wire.FeedbackListResult{}, err
	}
	out := wire.FeedbackListResult{Items: []wire.Feedback{}}
	for _, f := range feedbackOf(l.Meta.Meta) {
		out.Items = append(out.Items, f)
	}
	sort.Slice(out.Items, func(i, j int) bool {
		if out.Items[i].At != out.Items[j].At {
			return out.Items[i].At < out.Items[j].At
		}
		return out.Items[i].Entry < out.Items[j].Entry // 同一毫秒：按条目 id，顺序稳定
	})
	return out, nil
}

func feedbackOf(meta json.RawMessage) map[string]wire.Feedback {
	var m struct {
		F map[string]wire.Feedback `json:"feedback"`
	}
	_ = json.Unmarshal(meta, &m)
	if m.F == nil {
		m.F = map[string]wire.Feedback{}
	}
	return m.F
}

// EventDeleted 会话删除后的易失总线事件：连接据此发 sessions.changed {deleted}，订阅了它的连接收到 session.closed {reason: deleted}。
const EventDeleted = "session_deleted"

// Delete 删除会话及其子会话（id 以 "<session>/" 开头的子智能体、工作组成员）；分叉出去的会话是独立会话，不删。
// 有进行中的回合返回 not_idle（先 session.cancel）。重发：已删除返回 not_found。
func (v *App) Delete(ctx context.Context, in wire.SessionArgs) (wire.Empty, error) {
	if err := v.exists(ctx, in.Session); err != nil {
		return wire.Empty{}, err
	}
	if strings.Contains(in.Session, "/") {
		return wire.Empty{}, invalid("child sessions are deleted with their top-level session")
	}
	ids, err := v.tree(ctx, in.Session)
	if err != nil {
		return wire.Empty{}, err
	}
	for _, id := range ids {
		if hd, ok := v.o.Host.Get(id); ok && hd.InTurn() {
			return wire.Empty{}, fmt.Errorf("%w: session %s has a turn in progress", session.ErrNotIdle, id)
		}
	}
	// 子会话先删：顶层删到一半失败时，重试还能找到剩下的子会话。
	for i := len(ids) - 1; i >= 0; i-- {
		id := ids[i]
		if hd, ok := v.o.Host.Get(id); ok {
			if err := hd.Do(host.Command{Kind: host.Close, Text: "deleted"}); err != nil && !errors.Is(err, host.ErrClosed) {
				return wire.Empty{}, err
			}
			<-hd.Done()
		}
		if err := v.o.DB.DeleteSession(ctx, id, v.o.Cleaners); err != nil && !errors.Is(err, store.ErrNotFound) {
			return wire.Empty{}, err
		}
		v.streams.Delete(id)
		v.lives.Delete(id)
		v.o.Bus.Publish(bus.Envelope{Session: id, Kind: EventDeleted})
	}
	return wire.Empty{}, nil
}

// tree 顶层会话与它的子会话 id（顶层在前）。
func (v *App) tree(ctx context.Context, top string) ([]string, error) {
	out := []string{top}
	var before int64
	for {
		page, err := v.o.DB.ListSessions(ctx, before, 500, false)
		if err != nil {
			return nil, err
		}
		for _, s := range page {
			if strings.HasPrefix(s.ID, top+"/") {
				out = append(out, s.ID)
			}
		}
		if len(page) < 500 {
			return out, nil
		}
		before = page[len(page)-1].UpdatedAt
	}
}

// wireRun 会话行的 run_state（idle / running / suspended）→ 线上只有 idle / running。
func wireRun(s string) string {
	if s == "running" {
		return wire.RunRunning
	}
	return wire.RunIdle
}

// Create 新建会话并返回快照。session 缺省时由 client_id 派生，都缺省时生成；已存在返回 conflict（exists），
// 同一 client_id 重发返回已建会话的快照。
func (v *App) Create(ctx context.Context, in wire.CreateArgs) (wire.SessionSnapshot, error) {
	if v.o.Host.New == nil {
		return wire.SessionSnapshot{}, Errorf(wire.CodeInternal, "appapi: session builder not set")
	}
	id := in.Session
	switch {
	case id != "":
	case in.ClientID != "":
		id = derivedID("s-", in.ClientID)
	default:
		id = "s-" + host.NewItemID()
	}
	if err := validID(id); err != nil {
		return wire.SessionSnapshot{}, err
	}
	title, err := createTitle(in)
	if err != nil {
		return wire.SessionSnapshot{}, err
	}
	var meta json.RawMessage
	if bm := createMeta(in); len(bm) > 0 {
		meta, _ = json.Marshal(bm)
	}
	if _, err := v.o.Host.Create(ctx, id, in.Definition, meta); err != nil {
		if !errors.Is(err, store.ErrConflict) {
			return wire.SessionSnapshot{}, err
		}
		if in.ClientID != "" && v.createdBy(ctx, id, in.ClientID) {
			return v.snapshot(ctx, id)
		}
		return wire.SessionSnapshot{}, conflict(wire.ConflictExists, "session %s already exists", id)
	}
	if title != "" { // 初始标题（docs/57 4.2 钩子 3）：有标题后 autoTitle 不再改它
		if ms, ok := v.o.DB.(store.SessionMetaStore); ok {
			if err := ms.UpdateSessionMeta(ctx, id, &title, nil); err != nil {
				return wire.SessionSnapshot{}, err
			}
		}
	}
	return v.snapshot(ctx, id)
}

// 会话行业务键里的来源与引用（session.create {source, ref}）。
const (
	metaSource = "source"
	metaRef    = "ref"
)

// createMeta session.create 记在会话行业务键里的项（client_id、source、ref）。
func createMeta(in wire.CreateArgs) map[string]string {
	m := map[string]string{}
	if in.ClientID != "" {
		m[metaClientID] = in.ClientID
	}
	if in.Source != "" {
		m[metaSource] = in.Source
	}
	if in.Ref != "" {
		m[metaRef] = in.Ref
	}
	return m
}

// createTitle 校验 session.create 的 source / ref / title，返回整理后的标题。
func createTitle(in wire.CreateArgs) (string, error) {
	for name, s := range map[string]string{"source": in.Source, "ref": in.Ref} {
		if len(s) > 128 {
			return "", invalid("%s exceeds 128 bytes", name)
		}
		for _, r := range s {
			if r < 0x20 || r == 0x7f {
				return "", invalid("%s must not contain control characters", name)
			}
		}
	}
	t := strings.TrimSpace(in.Title)
	if len(t) > MaxTitle {
		return "", invalid("title exceeds %d bytes", MaxTitle)
	}
	for _, r := range t {
		if r < 0x20 || r == 0x7f {
			return "", invalid("title must not contain newlines or control characters")
		}
	}
	return t, nil
}

// createdBy 会话是否由带这个 client_id 的 session.create 建的。
func (v *App) createdBy(ctx context.Context, id, clientID string) bool {
	l, err := host.LoadHead(ctx, v.o.DB, id)
	if err != nil {
		return false
	}
	var m map[string]any
	_ = json.Unmarshal(l.Meta.Meta, &m)
	s, _ := m[metaClientID].(string)
	return s == clientID
}

// derivedID 由客户端幂等键派生会话 id（同一个键总得到同一个 id）。
func derivedID(prefix, key string) string {
	sum := sha256.Sum256([]byte(key))
	return prefix + hex.EncodeToString(sum[:12])
}

// validID 客户端给的会话 id：不能带 /（子会话 id 形如 "<顶层>/<子>"，由内核分配）、控制字符，不超过 128 字节。
func validID(id string) error {
	if len(id) > 128 || strings.ContainsAny(id, `/\`) {
		return invalid("invalid session id %q: must not contain / or \\ and must be at most 128 bytes", id)
	}
	for _, r := range id {
		if r < 0x20 || r == 0x7f {
			return invalid("session id must not contain control characters")
		}
	}
	return nil
}

// Get 会话快照（取代 session.open）：只读已提交数据，不打开会话；不存在返回 not_found。
func (v *App) Get(ctx context.Context, in wire.SessionArgs) (wire.SessionSnapshot, error) {
	if err := need(in.Session); err != nil {
		return wire.SessionSnapshot{}, err
	}
	return v.snapshot(ctx, in.Session)
}

// snapshot 会话头、运行状态与游标来自同一次读取（06 10.2.2 规则 4）：之后的变化一定在 cursor 之后。
// 未结束的交互在 S6 补上，live 在 S5 补上。
func (v *App) snapshot(ctx context.Context, id string) (wire.SessionSnapshot, error) {
	l, err := host.LoadHead(ctx, v.o.DB, id)
	if err != nil {
		return wire.SessionSnapshot{}, err
	}
	h := l.Snapshot.Header
	modeHdr := h // 档位持久化在顶层会话头上（子会话跟随顶层）
	if top := topOf(id); top != id {
		if tl, err := host.LoadHead(ctx, v.o.DB, top); err == nil {
			modeHdr = tl.Snapshot.Header
		}
	}
	var lastEnd *wire.TurnEnded
	if h.Status == session.StatusIdle { // 运行中不给：上一回合的结束原因已经过期
		lastEnd = WireLastEnd(l.LastEnd)
	}
	src, ref := sourceOf(l.Meta.Meta)
	return wire.SessionSnapshot{ID: id, Parent: l.Meta.ParentID, Origin: l.Meta.Origin, Title: l.Meta.Title, Pinned: pinnedOf(l.Meta.Meta),
		Source: src, Ref: ref, Agent: l.Meta.AgentID,
		Archived: store.ArchivedOf(l.Meta.Meta), RunState: WireRunState(h.Status), Mode: v.committedMode(id, modeHdr), Settings: settingsOf(h),
		Log: wire.LogPos{Base: h.CompactionBase, Len: h.LogLen}, Interactions: v.openInteractions(ctx, id), Cursor: l.Cursor,
		Live: v.live(id, h), LastEnd: lastEnd, Queued: v.queuedOf(ctx, id)}, nil
}

// live 进行中回合当前步的流式状态（读内存、不承诺与 cursor 一致，10.2.2 规则 7）；空闲时不给。
func (v *App) live(id string, h session.Header) *wire.Live {
	if h.Status == session.StatusIdle {
		return nil
	}
	if l := v.liveOf(id); l != nil {
		return l.snapshot()
	}
	return nil
}

// committedMode 快照里的档位：已提交值（06 10.2.2 规则 5）。内存里已切换、还没落库时给落库的那个，
// 标签从档位表取（沙箱级别只有当前档位才有，这时留空，客户端以随后的 mode_changed 为准）。
func (v *App) committedMode(id string, h session.Header) *wire.Mode {
	m := v.o.Modes
	if m == nil {
		return nil
	}
	st := m.Get(id)
	key, _ := m.ExtKeys()
	if saved, ok := h.Ext[key].(string); ok && saved != "" && saved != st.Mode {
		st = ModeState{Mode: saved, Label: saved}
		for _, d := range m.List() {
			if d.ID == saved {
				st.Label = d.Label
			}
		}
	}
	return &wire.Mode{Mode: st.Mode, Label: st.Label, Sandbox: st.Sandbox}
}

func settingsOf(h session.Header) wire.Settings {
	if h.Config == nil {
		return wire.Settings{}
	}
	st := wire.Settings{MaxSteps: h.Config.Policy.MaxSteps, Model: h.Config.Model}
	if r := h.Config.Params.Reasoning; r != nil {
		st.Reasoning = *r
	}
	if workmode.Of(h.Ext) == workmode.Plan { // M12：execute 不发（omitempty），和没设过的会话一样
		st.WorkMode = wire.WorkModePlan
	}
	return st
}

// Entries 读已提交日志，条目按 10.3 翻译；工具结果超过 MaxWireText 时截断，有工件存储时给全文引用。
func (v *App) Entries(ctx context.Context, in wire.EntriesArgs) (wire.EntriesResult, error) {
	if err := need(in.Session); err != nil {
		return wire.EntriesResult{}, err
	}
	l, err := host.LoadHead(ctx, v.o.DB, in.Session) // 只要头部：翻页不全量反序列化（审查 P1-35）
	if err != nil {
		return wire.EntriesResult{}, err
	}
	h := l.Snapshot.Header
	from, to := h.CompactionBase, h.LogLen
	if in.Turn != nil { // 客户端改进 M8 协议T15：只取这个回合
		if in.From != nil || in.To != nil || in.Tail > 0 {
			return wire.EntriesResult{}, invalid("turn is mutually exclusive with from, to and tail")
		}
		lo, hi, ok, err := v.turnRange(ctx, in.Session, *in.Turn)
		if err != nil {
			return wire.EntriesResult{}, err
		}
		if !ok {
			return wire.EntriesResult{}, invalid("turn %d does not exist", *in.Turn)
		}
		from, to = lo, hi
	}
	if in.From != nil {
		from = *in.From
	}
	if in.To != nil {
		to = *in.To
	}
	if in.Tail > 0 {
		from, to = max(h.LogLen-in.Tail, 0), h.LogLen
	}
	if from < 0 || to < from {
		return wire.EntriesResult{}, invalid("invalid range [%d, %d)", from, to)
	}
	es, err := v.o.DB.Entries(ctx, in.Session, from, to)
	if err != nil {
		return wire.EntriesResult{}, err
	}
	out := wire.EntriesResult{Entries: make([]wire.Entry, len(es)), Base: h.CompactionBase, Len: h.LogLen}
	spill := v.spill(in.Session)
	for i, e := range es {
		out.Entries[i] = WireEntry(from+i, e, spill)
	}
	return out, nil
}

// spill 把超长工具结果全文存成工件（内容寻址，重复存无害）。
func (v *App) spill(id string) Spill {
	art := v.o.Artifacts
	if art == nil {
		return nil
	}
	return func(text string) (string, bool) {
		h, err := art.Put(context.Background(), id, "text/plain", []byte(text))
		if err != nil {
			return "", false
		}
		return toolhub.ArtifactPrefix + h, true
	}
}

// —— 输入与回合 ——

// Send 投递用户输入（落 inbox 即返回）。client_id 是幂等键：重发同一个只算一次。queued 表示投递时有进行中的回合：
// 输入不开新回合，而是在这个回合的下一次调模型前插入，或排到回合结束后。
func (v *App) Send(ctx context.Context, in wire.SendArgs) (wire.SendResult, error) {
	if err := need(in.Session); err != nil {
		return wire.SendResult{}, err
	}
	if err := needClientID(in.ClientID); err != nil {
		return wire.SendResult{}, err
	}
	content, err := v.content(in.Content, in.Text)
	if err != nil {
		return wire.SendResult{}, err
	}
	hd, err := v.o.Host.OpenExisting(ctx, in.Session)
	if err != nil {
		return wire.SendResult{}, err
	}
	queued := hd.InTurn()
	// queue（M12 协议T20）：存储不支持排队时忽略，退回原行为（回合中插入）。
	if err := hd.Do(host.Command{Kind: host.Send, ItemID: in.ClientID, Content: content, Queue: in.Queue && v.queueOK()}); err != nil {
		return wire.SendResult{}, err
	}
	v.unarchive(ctx, in.Session) // 往已归档的会话发消息自动恢复（客户端改进 M7）
	v.autoTitle(ctx, in.Session, content)
	return wire.SendResult{ClientID: in.ClientID, Queued: queued}, nil
}

func needClientID(id string) error {
	if id == "" {
		return Errorf(wire.CodeBadRequest, "missing client_id")
	}
	if len(id) > 128 {
		return invalid("client_id exceeds 128 bytes")
	}
	return nil
}

// content 输入部件 → 内核内容。只收文本与媒体；媒体必须引用本服务的工件（先 artifact.put 再引用）。
func (v *App) content(parts []wire.Part, text string) (session.Content, error) {
	if (len(parts) > 0) == (text != "") {
		return nil, Errorf(wire.CodeBadRequest, "exactly one of content and text is required")
	}
	if text != "" {
		return session.Content{session.NewTextPart(text)}, nil
	}
	out := make(session.Content, 0, len(parts))
	for i, p := range parts {
		switch p.Kind {
		case "text":
			out = append(out, session.NewTextPart(p.Text))
		case "media":
			m := p.Media
			if m == nil || m.MIME == "" {
				return nil, invalid("part %d: media requires ref and mime", i+1)
			}
			hash, ok := artifactHash(m.Ref)
			if !ok {
				return nil, invalid("part %d: ref must be %s<sha256>", i+1, toolhub.ArtifactPrefix)
			}
			art := v.o.Artifacts
			if art == nil { // 没有工件存储就核对不了引用，也取不回内容
				return nil, Errorf(wire.CodeUnsupported, "no artifact store; cannot send media")
			}
			data, err := art.Get(hash)
			if err != nil {
				return nil, invalid("part %d: artifact %s does not exist", i+1, m.Ref)
			}
			// 大小以实际内容为准，不信客户端报的；引用统一成小写哈希。
			out = append(out, session.Part{Kind: "media", Media: &session.MediaRef{URI: toolhub.ArtifactPrefix + hash,
				MIME: m.MIME, Bytes: uint64(len(data)), SHA256: hash}})
		default:
			return nil, invalid("part %d: unsupported kind %q (only text and media)", i+1, p.Kind)
		}
	}
	return out, nil
}

// artifactHash 解析 artifact://<sha256>；只认 64 位十六进制（哈希会拼进工件文件路径，不能放过别的字符）。
func artifactHash(ref string) (string, bool) {
	h, ok := strings.CutPrefix(ref, toolhub.ArtifactPrefix)
	if !ok || len(h) != 64 {
		return "", false
	}
	if _, err := hex.DecodeString(h); err != nil {
		return "", false
	}
	return strings.ToLower(h), true
}

// Steer 回合中插话（下一步生效）。client_id 是幂等键：同一回合重发只插一次。没有进行中的回合返回 invalid。
func (v *App) Steer(ctx context.Context, in wire.SteerArgs) (wire.Empty, error) {
	if err := need(in.Session); err != nil {
		return wire.Empty{}, err
	}
	if err := needClientID(in.ClientID); err != nil {
		return wire.Empty{}, err
	}
	if in.Text == "" {
		return wire.Empty{}, Errorf(wire.CodeBadRequest, "missing text")
	}
	hd, err := v.running(ctx, in.Session)
	if err != nil {
		return wire.Empty{}, err
	}
	return wire.Empty{}, hd.Do(host.Command{Kind: host.Steer, Text: in.Text, ItemID: in.ClientID})
}

// Cancel 取消进行中的回合；没有回合时什么都不做（本身幂等）。
func (v *App) Cancel(ctx context.Context, in wire.SessionArgs) (wire.Empty, error) {
	if err := need(in.Session); err != nil {
		return wire.Empty{}, err
	}
	hd, err := v.running(ctx, in.Session)
	if errors.Is(err, session.ErrNotWaiting) {
		return wire.Empty{}, nil
	}
	if err != nil {
		return wire.Empty{}, err
	}
	return wire.Empty{}, hd.Do(host.Command{Kind: host.Cancel})
}

// running 已打开会话的句柄：会话存在但没打开说明没有进行中的回合（session.ErrNotWaiting），不存在返回 not_found。
// 不为插话、取消去打开会话。
func (v *App) running(ctx context.Context, id string) (host.Handle, error) {
	if hd, ok := v.o.Host.Get(id); ok {
		return hd, nil
	}
	ok, err := v.o.DB.Exists(ctx, id)
	if err != nil {
		return host.Handle{}, err
	}
	if !ok {
		return host.Handle{}, fmt.Errorf("%w: %s", host.ErrSessionNotFound, id)
	}
	return host.Handle{}, session.ErrNotWaiting
}

// idleHandle 打开已有会话，要求没有进行中的回合（需空闲的命令用）。
func (v *App) idleHandle(ctx context.Context, id string) (host.Handle, error) {
	hd, err := v.o.Host.OpenExisting(ctx, id)
	if err != nil {
		return host.Handle{}, err
	}
	if hd.InTurn() {
		return host.Handle{}, fmt.Errorf("%w: session %s has a turn in progress", session.ErrNotIdle, id)
	}
	return hd, nil
}

// —— 改日志 ——

// Rewind 保留 to_turn 之前的回合，截掉 to_turn 及之后（绝对目标：目标回合已不存在时什么都不截）。
// 需空闲；if_cursor 不符返回 conflict（stale）。
func (v *App) Rewind(ctx context.Context, in wire.RewindArgs) (wire.RewindResult, error) {
	if err := need(in.Session); err != nil {
		return wire.RewindResult{}, err
	}
	if in.ToTurn == 0 {
		return wire.RewindResult{}, invalid("to_turn must be >= 1")
	}
	if in.RestoreFiles && v.o.Changes == nil {
		return wire.RewindResult{}, Errorf(wire.CodeUnsupported, "checkpoint plugin not installed; cannot revert files")
	}
	hd, err := v.idleHandle(ctx, in.Session)
	if err != nil {
		return wire.RewindResult{}, err
	}
	if in.RestoreFiles {
		return v.rewindRestore(ctx, hd, in)
	}
	var to int
	if err := hd.Do(host.Command{Kind: host.Rewind, ToTurn: in.ToTurn, IfCursor: in.IfCursor, RewoundTo: &to}); err != nil {
		return wire.RewindResult{}, err
	}
	return wire.RewindResult{To: to}, nil
}

// Compact 手动压缩到 upto（缺省按保留尾部规则选）；已压缩到该位置时直接返回那次压缩。需空闲；if_cursor 不符返回 conflict（stale）。
func (v *App) Compact(ctx context.Context, in wire.CompactArgs) (wire.CompactResult, error) {
	if err := need(in.Session); err != nil {
		return wire.CompactResult{}, err
	}
	if v.o.Compactor == nil {
		return wire.CompactResult{}, Errorf(wire.CodeUnsupported, "compact-basic plugin not installed")
	}
	hd, err := v.idleHandle(ctx, in.Session)
	if err != nil {
		return wire.CompactResult{}, err
	}
	l, err := v.o.DB.Load(ctx, in.Session, nil)
	if err != nil {
		return wire.CompactResult{}, err
	}
	if in.IfCursor != nil && l.Cursor != *in.IfCursor { // 先拒掉，免得白花一次总结；actor 执行前还会再核对
		return wire.CompactResult{}, fmt.Errorf("%w: expected %d, current %d", host.ErrStale, *in.IfCursor, l.Cursor)
	}
	h := l.Snapshot.Header
	if in.Upto != nil && *in.Upto == h.CompactionBase && h.CompactionBase > 0 {
		return wire.CompactResult{Summary: lastSummary(l.Snapshot), Upto: h.CompactionBase}, nil
	}
	core, err := session.Restore(l.Snapshot)
	if err != nil {
		return wire.CompactResult{}, err
	}
	c := v.o.Compactor
	var view *reqlog.CompactionView
	var meta map[string]any
	if in.Upto != nil {
		if !core.CanCompact(*in.Upto) {
			return wire.CompactResult{}, invalid("upto=%d is not a valid compaction point (cannot split a tool call from its result or precede the current point %d)", *in.Upto, h.CompactionBase)
		}
		view, meta, err = c.At(ctx, in.Session, core, *in.Upto)
	} else if view, meta, err = c.Plan(ctx, in.Session, core, true); err == nil && view == nil {
		view, meta, err = c.At(ctx, in.Session, core, h.LogLen)
	}
	if err != nil {
		return wire.CompactResult{}, err
	}
	if view == nil {
		return wire.CompactResult{}, invalid("nothing to compact")
	}
	sum := ""
	for _, p := range view.Summary {
		sum += p.Text
	}
	if err := hd.Do(host.Command{Kind: host.Compact, Text: sum, Upto: view.Upto, IfCursor: in.IfCursor}); err != nil {
		return wire.CompactResult{}, err
	}
	return wire.CompactResult{Summary: sum, Upto: view.Upto, TokensBefore: intOf(meta["tokens_before"]),
		TokensAfter: intOf(meta["tokens_after"])}, nil
}

// lastSummary 当前压缩点那次压缩的总结（压缩条目写在压缩点之后，在加载窗口里）。
func lastSummary(s session.Snapshot) string {
	for i := len(s.Entries) - 1; i >= 0; i-- {
		if c := s.Entries[i].Item.Compaction; c != nil && c.Upto == s.Header.CompactionBase {
			out := ""
			for _, p := range c.Summary {
				out += p.Text
			}
			return out
		}
	}
	return ""
}

// Fork 从会话分出新会话，返回新会话快照。to 缺省时由 client_id 派生（同一 client_id 重发返回已分出的会话），
// 都缺省时生成。来源须空闲；if_cursor 不符返回 conflict（stale）；目标已存在返回 conflict（exists）。
func (v *App) Fork(ctx context.Context, in wire.ForkArgs) (wire.SessionSnapshot, error) {
	if err := need(in.Session); err != nil {
		return wire.SessionSnapshot{}, err
	}
	if in.Origin != "" && in.Origin != "fork" && in.Origin != "side" {
		return wire.SessionSnapshot{}, invalid("origin must be fork or side, got %q", in.Origin)
	}
	to := in.To
	switch {
	case to != "":
	case in.ClientID != "":
		to = derivedID("fork-", in.Session+"\x00"+in.ClientID)
	default:
		to = "fork-" + host.NewItemID()
	}
	if err := validID(to); err != nil {
		return wire.SessionSnapshot{}, err
	}
	if hd, ok := v.o.Host.Get(in.Session); ok && hd.InTurn() {
		return wire.SessionSnapshot{}, fmt.Errorf("%w: source session %s has a turn in progress", session.ErrNotIdle, in.Session)
	}
	upto := -1
	if in.Upto != nil {
		if in.UptoTurn != nil {
			return wire.SessionSnapshot{}, invalid("upto and upto_turn are mutually exclusive")
		}
		upto = *in.Upto
	}
	if in.UptoTurn != nil { // 客户端改进 M8 协议T15：分叉到第 N 回合起点（不含第 N 回合）
		u, err := v.uptoTurn(ctx, in.Session, *in.UptoTurn)
		if err != nil {
			return wire.SessionSnapshot{}, err
		}
		upto = u
	}
	if _, err := v.o.Host.ForkIf(ctx, in.Session, to, upto, in.Origin, in.IfCursor); err != nil {
		if !errors.Is(err, store.ErrConflict) {
			return wire.SessionSnapshot{}, err
		}
		if in.ClientID != "" {
			if l, e := host.LoadHead(ctx, v.o.DB, to); e == nil && l.Meta.ParentID == in.Session {
				return v.snapshot(ctx, to)
			}
		}
		return wire.SessionSnapshot{}, conflict(wire.ConflictExists, "session %s already exists", to)
	}
	return v.snapshot(ctx, to)
}

// turnRange 回合 turn 的条目区间 [from, to)：存储实现了 store.TurnRangeStore 就走它（sqlite 按索引查），
// 否则扫全部条目兜底。没有这个回合时 ok = false。
func (v *App) turnRange(ctx context.Context, id string, turn uint32) (int, int, bool, error) {
	if tr, ok := v.o.DB.(store.TurnRangeStore); ok {
		return tr.TurnRange(ctx, id, turn)
	}
	es, err := v.o.DB.Entries(ctx, id, 0, -1)
	if err != nil {
		return 0, 0, false, err
	}
	from, to, ok := 0, 0, false
	for i, e := range es {
		if e.Turn != turn {
			continue
		}
		if !ok {
			from = i
		}
		to, ok = i+1, true
	}
	return from, to, ok, nil
}

// uptoTurn fork {upto_turn: n} 换算成 upto：第 n 回合首条条目的下标；n = 最后一回合 + 1 时为 -1（整个会话）。
// n < 1、n > 最后一回合 + 1 或日志里没有第 n 回合返回 invalid。
func (v *App) uptoTurn(ctx context.Context, id string, n uint32) (int, error) {
	l, err := host.LoadHead(ctx, v.o.DB, id)
	if err != nil {
		return 0, err
	}
	var last uint32
	if ln := l.Snapshot.Header.LogLen; ln > 0 {
		es, err := v.o.DB.Entries(ctx, id, ln-1, ln)
		if err != nil {
			return 0, err
		}
		if len(es) > 0 {
			last = es[0].Turn
		}
	}
	if n < 1 || n > last+1 {
		return 0, invalid("upto_turn=%d out of range [1, %d]", n, last+1)
	}
	if n == last+1 {
		return -1, nil
	}
	from, _, ok, err := v.turnRange(ctx, id, n)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, invalid("turn %d does not exist", n)
	}
	return from, nil
}

// —— 查询与设置 ——

// exists 会话存在，否则 not_found。
func (v *App) exists(ctx context.Context, id string) error {
	if err := need(id); err != nil {
		return err
	}
	ok, err := v.o.DB.Exists(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s", host.ErrSessionNotFound, id)
	}
	return nil
}

// Stats 会话统计（只读投影）。
func (v *App) Stats(ctx context.Context, in wire.SessionArgs) (wire.Stats, error) {
	if err := v.exists(ctx, in.Session); err != nil {
		return wire.Stats{}, err
	}
	st, err := v.o.DB.SessionStats(ctx, in.Session)
	if err != nil {
		return wire.Stats{}, err
	}
	out := wire.Stats{Attempts: st.Attempts, Reported: st.Reported, Missing: st.Missing, Errors: st.Errors,
		Input: st.Input, Output: st.Output, CacheRead: st.CacheRead, CacheWrite: st.CacheWrite, HitRate: st.HitRate(),
		ToolCalls: st.ToolCalls, Compacts: st.Compacts, LastRequest: st.LastRequest}
	if len(st.Composition) > 0 && json.Valid(st.Composition) {
		out.Composition = st.Composition
	}
	return out, nil
}

// Settings 查看或修改会话设置（取代 session.max_steps）。空闲时等落库后返回 committed=true；
// 回合中只改内存、到步边界生效，返回 committed=false，落库时发 session.settings_changed（10.2.2 规则 5）。
func (v *App) Settings(ctx context.Context, in wire.SettingsArgs) (wire.SettingsResult, error) {
	if err := need(in.Session); err != nil {
		return wire.SettingsResult{}, err
	}
	l, err := host.LoadHead(ctx, v.o.DB, in.Session)
	if err != nil {
		return wire.SettingsResult{}, err
	}
	cur := settingsOf(l.Snapshot.Header)
	if in.MaxSteps == nil && in.Model == nil && in.Reasoning == nil && in.WorkMode == nil {
		return wire.SettingsResult{Settings: cur, Committed: true}, nil
	}
	if in.WorkMode != nil && *in.WorkMode != "" && !workmode.Valid(*in.WorkMode) {
		return wire.SettingsResult{}, invalid("unknown work mode %q (execute or plan)", *in.WorkMode)
	}
	if in.Model != nil {
		if v.o.Models == nil {
			return wire.SettingsResult{}, Errorf(wire.CodeUnsupported, "no model catalog")
		}
		if err := v.o.Models.Check(*in.Model); err != nil {
			return wire.SettingsResult{}, invalid("model %q unavailable: %v", *in.Model, err)
		}
	}
	hd, err := v.o.Host.OpenExisting(ctx, in.Session)
	if err != nil {
		return wire.SettingsResult{}, err
	}
	if in.Reasoning != nil && *in.Reasoning != "" && !wire.ValidReasoning(*in.Reasoning) {
		return wire.SettingsResult{}, invalid("unknown reasoning effort %q (one of %s)", *in.Reasoning, strings.Join(wire.ReasoningEfforts, " / "))
	}
	ok := true
	if in.MaxSteps != nil || in.Model != nil || in.Reasoning != nil {
		if ok, err = v.apply(in.Session, hd, host.Command{Kind: host.SetConfig, MaxSteps: in.MaxSteps, Model: in.Model, Reasoning: in.Reasoning}); err != nil {
			return wire.SettingsResult{}, err
		}
	}
	if in.WorkMode != nil { // M12 Plan 模式：存在会话头 Ext（execute 删键），不碰权限档位
		wok, err := v.apply(in.Session, hd, host.Command{Kind: host.SetExt, Key: workmode.ExtKey, Value: workmode.ExtValue(*in.WorkMode)})
		if err != nil {
			return wire.SettingsResult{}, err
		}
		ok = ok && wok
		cur.WorkMode = ""
		if *in.WorkMode == workmode.Plan {
			cur.WorkMode = wire.WorkModePlan
		}
	}
	if in.MaxSteps != nil {
		cur.MaxSteps = *in.MaxSteps
	}
	if in.Model != nil {
		cur.Model = *in.Model
	}
	if in.Reasoning != nil {
		cur.Reasoning = *in.Reasoning
	}
	return wire.SettingsResult{Settings: cur, Committed: ok}, nil
}

// WorkspaceList 当前与最近打开的工作区。没有工作区管理器时只报当前目录。
func (v *App) WorkspaceList(_ context.Context, _ wire.Empty) (wire.WorkspaceListResult, error) {
	if v.o.Workspaces == nil {
		return wire.WorkspaceListResult{Items: []wire.WorkspaceInfo{v.selfWorkspace()}}, nil
	}
	items := v.o.Workspaces.List()
	if items == nil {
		items = []wire.WorkspaceInfo{}
	}
	return wire.WorkspaceListResult{Items: items}, nil
}

// workspaceKey 当前工作区的键（Host.Workspace）；宿主不按工作区分时为空。
func (v *App) workspaceKey() string {
	if v.o.Host != nil {
		return v.o.Host.Workspace
	}
	return ""
}

// selfWorkspace 没有工作区管理器时按本 App 的目录拼出当前工作区。id 按工作区键算（与 hello.workspace_id 一致），
// 没有键时按工作目录算。
func (v *App) selfWorkspace() wire.WorkspaceInfo {
	key := v.workspaceKey()
	if key == "" {
		key = v.o.Workdir
	}
	return wire.WorkspaceInfo{ID: wire.WorkspaceID(key), Path: v.o.Workdir, Name: filepath.Base(v.o.Workdir), Trusted: v.o.Trusted, Current: true}
}

// Locate session.locate（Mortise docs/72 B2）：会话所属工作区；给了 entry 时报它所在回合与是否已被回退。
// 不按当前工作区过滤（深链要先知道会话在哪个工作区才能决定切换还是只读查看）。
func (v *App) Locate(ctx context.Context, in wire.LocateArgs) (wire.LocateResult, error) {
	if err := need(in.Session); err != nil {
		return wire.LocateResult{}, err
	}
	l, err := host.LoadHead(ctx, v.o.DB, in.Session)
	if err != nil {
		return wire.LocateResult{}, err
	}
	out := wire.LocateResult{Workspace: l.Meta.Workspace, WorkspaceID: wire.WorkspaceID(l.Meta.Workspace)}
	if in.Entry == "" {
		return out, nil
	}
	turn, rewound, found, err := v.entryTurn(ctx, in.Session, in.Entry)
	if err != nil {
		return wire.LocateResult{}, err
	}
	if !found {
		return wire.LocateResult{}, Errorf(wire.CodeNotFound, "entry %s not found in session %s", in.Entry, in.Session)
	}
	out.Turn, out.Rewound = turn, rewound
	return out, nil
}

// entryTurn 条目所在回合：存储实现了 store.EntryLocator 就走它（含回退归档）；否则扫当前日志兜底，
// 已回退的条目找不到（ok = false）。
func (v *App) entryTurn(ctx context.Context, id, entryID string) (uint32, bool, bool, error) {
	if el, ok := v.o.DB.(store.EntryLocator); ok {
		return el.EntryTurn(ctx, id, entryID)
	}
	es, err := v.o.DB.Entries(ctx, id, 0, -1)
	if err != nil {
		return 0, false, false, err
	}
	for _, e := range es {
		if e.ID == entryID {
			return e.Turn, false, true, nil
		}
	}
	return 0, false, false, nil
}

// WorkspaceOpen 切换工作区。有会话正在跑回合时拒绝（not_idle）：切换会关掉本工作区的运行时，进行中的回合会被打断。
func (v *App) WorkspaceOpen(ctx context.Context, in wire.WorkspaceOpenArgs) (wire.WorkspaceOpenResult, error) {
	if v.o.Workspaces == nil {
		return wire.WorkspaceOpenResult{}, Errorf(wire.CodeUnsupported, "this app does not support switching workspaces")
	}
	if strings.TrimSpace(in.Path) == "" {
		return wire.WorkspaceOpenResult{}, invalid("path must not be empty")
	}
	if n, err := v.runningSessions(ctx); err != nil {
		return wire.WorkspaceOpenResult{}, err
	} else if n > 0 {
		return wire.WorkspaceOpenResult{}, Errorf(wire.CodeNotIdle, "%d session(s) running: stop them or wait before switching workspaces", n)
	}
	ws, err := v.o.Workspaces.Open(ctx, in.Path)
	if err != nil {
		return wire.WorkspaceOpenResult{}, invalid("cannot open workspace: %v", err)
	}
	return wire.WorkspaceOpenResult{Workspace: ws}, nil
}

// runningSessions 正在跑回合的会话数（含子会话）。
func (v *App) runningSessions(ctx context.Context) (int, error) {
	ids, err := v.runningIDs(ctx)
	return len(ids), err
}

// runningIDs 正在跑回合的会话 id（含子会话）。
func (v *App) runningIDs(ctx context.Context) ([]string, error) {
	var out []string
	for before := int64(0); ; {
		rows, err := v.o.DB.ListSessionsIn(ctx, v.o.Host.Workspace, before, 200, false)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			if wireRun(r.RunState) == wire.RunRunning {
				out = append(out, r.ID)
			}
		}
		if len(rows) < 200 {
			return out, nil
		}
		before = rows[len(rows)-1].UpdatedAt
	}
}

// reloadCancelWait workspace.reload {force} 取消回合后最多等多久让它们停下（等不到也照样重建：回合中断，
// 下次打开时按恢复机制处理）。测试调小。
var reloadCancelWait = 5 * time.Second

// WorkspaceReload 在当前目录上重建运行时（客户端改进 M3：改自定义模型、默认模型、登录退出、插件开关后生效，不用重启进程）。
// 有回合在跑时返回 not_idle（details.sessions 列出会话）；force 时先取消这些回合再重建。新配置起不来、回到了原来的
// 配置时返回 invalid，details.reverted = true（运行时照常可用）。成功后旧连接随后断开，客户端重连并重新 hello。
func (v *App) WorkspaceReload(ctx context.Context, in wire.WorkspaceReloadArgs) (wire.WorkspaceReloadResult, error) {
	rl, ok := v.o.Workspaces.(Reloader)
	if v.o.Workspaces == nil || !ok {
		return wire.WorkspaceReloadResult{}, Errorf(wire.CodeUnsupported, "this app does not support rebuilding the runtime (CLI: exit and run again)")
	}
	ids, err := v.runningIDs(ctx)
	if err != nil {
		return wire.WorkspaceReloadResult{}, err
	}
	if len(ids) > 0 {
		if !in.Force {
			e := Errorf(wire.CodeNotIdle, "%d session(s) running: rebuilding interrupts them (use force to cancel and rebuild)", len(ids))
			e.Details = map[string]any{"running": len(ids), "sessions": ids}
			return wire.WorkspaceReloadResult{}, e
		}
		v.cancelAll(ctx, ids)
	}
	ws, err := rl.Reload(ctx)
	if err != nil {
		var rv *ReloadReverted
		if errors.As(err, &rv) {
			e := Errorf(wire.CodeInvalid, "%s", rv.Error())
			e.Details = map[string]any{"reverted": true}
			return wire.WorkspaceReloadResult{}, e
		}
		return wire.WorkspaceReloadResult{}, Errorf(wire.CodeInternal, "failed to rebuild runtime: %v", err)
	}
	return wire.WorkspaceReloadResult{Workspace: ws}, nil
}

// cancelAll 取消这些会话的回合，最多等 reloadCancelWait 让它们都停下。
func (v *App) cancelAll(ctx context.Context, ids []string) {
	for _, id := range ids {
		if hd, ok := v.o.Host.Get(id); ok {
			go func() { _ = hd.Do(host.Command{Kind: host.Cancel}) }() // 投递不阻塞：回合可能正等着人确认
		}
	}
	deadline := time.Now().Add(reloadCancelWait)
	for time.Now().Before(deadline) {
		if n, err := v.runningSessions(ctx); err != nil || n == 0 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// ModelsList 可选模型（别名在前）与新会话的缺省模型。没有装配目录时返回 unsupported。
func (v *App) ModelsList(_ context.Context, _ wire.Empty) (wire.ModelsListResult, error) {
	if v.o.Models == nil {
		return wire.ModelsListResult{}, Errorf(wire.CodeUnsupported, "no model catalog")
	}
	r := v.o.Models.List()
	if r.Models == nil {
		r.Models = []wire.ModelInfo{}
	}
	return r, nil
}

// apply 投递 SetExt / SetConfig 并等受理，最多等 applyWait：回合中这类命令要到步边界才受理，而这一步可能正等着
// 人确认，不能一直等。等不到就返回 committed=false：命令照样在步边界生效、随下个检查点落库；失败时发 notice。
func (v *App) apply(id string, hd host.Handle, c host.Command) (bool, error) {
	var ok bool
	c.Committed = &ok
	done := make(chan error, 1)
	go func() { done <- hd.Do(c) }()
	select {
	case err := <-done:
		return ok, err
	case <-time.After(applyWait):
		go func() {
			if err := <-done; err != nil {
				v.NoticeKey(id, wire.NoticeError, "settings", NoticeKeySettingsSaveFailed, map[string]string{"error": err.Error()},
					fmt.Sprintf("[settings not saved: %v]", err))
			}
		}()
		return false, nil
	}
}

// —— 档位 ——

func wireMode(s ModeState) wire.Mode {
	return wire.Mode{Mode: s.Mode, Label: s.Label, Sandbox: s.Sandbox}
}

// ModeList 全部档位。
func (v *App) ModeList(_ context.Context, _ wire.Empty) (wire.ModeListResult, error) {
	m, err := v.modes()
	if err != nil {
		return wire.ModeListResult{}, err
	}
	out := wire.ModeListResult{Modes: []wire.ModeInfo{}}
	for _, d := range m.List() {
		out.Modes = append(out.Modes, wire.ModeInfo{ID: d.ID, Label: d.Label, Short: d.Short, Builtin: d.Builtin, Project: d.Project, Widened: d.Widened,
			Description: d.Description})
	}
	// default（客户端改进 M10）：只给 modes 里有的 id，客户端可以直接拿它去 modes 里找标签。
	if dm, ok := m.(ModeDefaulter); ok {
		id := dm.DefaultMode()
		for _, d := range out.Modes {
			if d.ID == id {
				out.Default = id
			}
		}
	}
	return out, nil
}

// ModeGet 会话当前档位；committed 表示它已落库（回合中刚切换、还没到检查点时为 false）。
func (v *App) ModeGet(ctx context.Context, in wire.SessionArgs) (wire.ModeSetResult, error) {
	m, err := v.modes()
	if err != nil {
		return wire.ModeSetResult{}, err
	}
	if err := v.exists(ctx, in.Session); err != nil {
		return wire.ModeSetResult{}, err
	}
	st := m.Get(in.Session)
	committed := true
	if l, err := host.LoadHead(ctx, v.o.DB, topOf(in.Session)); err == nil {
		key, _ := m.ExtKeys()
		if saved, ok := l.Snapshot.Header.Ext[key].(string); ok && saved != st.Mode {
			committed = false
		}
	}
	return wire.ModeSetResult{State: wireMode(st), Committed: committed}, nil
}

// ModeSet 切换档位：立即对后续工具调用生效，持久化到顶层会话头。空闲时等落库，committed=true；
// 回合中到步边界落库，committed=false，落库时发 session.mode_changed（10.2.2 规则 5）。
func (v *App) ModeSet(ctx context.Context, in wire.ModeSetArgs) (wire.ModeSetResult, error) {
	m, err := v.modes()
	if err != nil {
		return wire.ModeSetResult{}, err
	}
	if in.Mode == "" {
		return wire.ModeSetResult{}, Errorf(wire.CodeBadRequest, "missing mode")
	}
	if err := v.exists(ctx, in.Session); err != nil { // 先查会话，再改内存档位
		return wire.ModeSetResult{}, err
	}
	top, err := v.o.Host.OpenExisting(ctx, topOf(in.Session))
	if err != nil {
		return wire.ModeSetResult{}, err
	}
	st, err := m.Set(in.Session, in.Mode, in.Confirm)
	if err != nil {
		return wire.ModeSetResult{}, err
	}
	key, _ := m.ExtKeys()
	ok, err := v.apply(in.Session, top, host.Command{Kind: host.SetExt, Key: key, Value: st.Mode})
	if err != nil {
		return wire.ModeSetResult{}, err
	}
	return wire.ModeSetResult{State: wireMode(st), Committed: ok}, nil
}

// ModeShow 档位的完整取值表；mode 缺省取会话的当前档位。
func (v *App) ModeShow(ctx context.Context, in wire.ModeShowArgs) (wire.ModeShowResult, error) {
	m, err := v.modes()
	if err != nil {
		return wire.ModeShowResult{}, err
	}
	if in.Session == "" && in.Mode == "" {
		return wire.ModeShowResult{}, Errorf(wire.CodeBadRequest, "missing session or mode")
	}
	mode := in.Mode
	if in.Session != "" {
		if err := v.exists(ctx, in.Session); err != nil {
			return wire.ModeShowResult{}, err
		}
		if mode == "" {
			mode = m.Get(in.Session).Mode
		}
	}
	items, err := m.Show(in.Session, mode)
	if err != nil {
		return wire.ModeShowResult{}, err
	}
	out := wire.ModeShowResult{Mode: mode, Items: []wire.ModeItem{}}
	for _, it := range items {
		out.Items = append(out.Items, wire.ModeItem{Item: it.Item, Title: it.Title, Value: it.Value, Source: it.Source, Bounded: it.Bounded,
			Class: it.Class, Type: it.Type, Owner: it.Owner, Tools: it.Tools, Levels: it.Levels, Allow: it.Allow, Ask: it.Ask, Deny: it.Deny,
			Overridden: it.Overridden})
	}
	v.modeDetail(m, &out) // docs/51 第 6 节：档位本身的信息（mode_edit.go）
	return out, nil
}

// —— 工件与 trace ——

// ArtifactGet 分段读工件（G10）：offset / length 按字节，单次最多 MaxArtifactChunk。文本类且这一段是完整 UTF-8 时给 text，
// 否则给 base64。
func (v *App) ArtifactGet(ctx context.Context, in wire.ArtifactGetArgs) (wire.ArtifactGetResult, error) {
	art := v.o.Artifacts
	if art == nil {
		return wire.ArtifactGetResult{}, Errorf(wire.CodeUnsupported, "no artifact store")
	}
	if err := v.exists(ctx, in.Session); err != nil {
		return wire.ArtifactGetResult{}, err
	}
	hash, ok := artifactHash(in.Ref)
	if !ok {
		return wire.ArtifactGetResult{}, invalid("ref must be %s<sha256>", toolhub.ArtifactPrefix)
	}
	data, err := art.Get(hash)
	if err != nil {
		return wire.ArtifactGetResult{}, Errorf(wire.CodeNotFound, "artifact %s does not exist", in.Ref)
	}
	if in.Offset < 0 || in.Length < 0 || in.Offset > len(data) {
		return wire.ArtifactGetResult{}, invalid("invalid range: offset=%d length=%d, artifact is %d bytes", in.Offset, in.Length, len(data))
	}
	n := in.Length
	if n == 0 || n > MaxArtifactChunk {
		n = MaxArtifactChunk
	}
	chunk := data[in.Offset:min(in.Offset+n, len(data))]
	out := wire.ArtifactGetResult{MIME: http.DetectContentType(data), Size: len(data)}
	if strings.HasPrefix(out.MIME, "text/") && utf8.Valid(chunk) {
		out.Text = string(chunk)
	} else {
		out.Base64 = base64.StdEncoding.EncodeToString(chunk)
	}
	return out, nil
}

// ArtifactPut 上传输入用的小文件（不超过 MaxArtifactPut；更大的走 HTTP 上传，10.9），返回 artifact:// 引用。
func (v *App) ArtifactPut(ctx context.Context, in wire.ArtifactPutArgs) (wire.ArtifactPutResult, error) {
	art := v.o.Artifacts
	if art == nil {
		return wire.ArtifactPutResult{}, Errorf(wire.CodeUnsupported, "no artifact store")
	}
	if err := v.exists(ctx, in.Session); err != nil {
		return wire.ArtifactPutResult{}, err
	}
	if in.MIME == "" || len(in.MIME) > 255 {
		return wire.ArtifactPutResult{}, Errorf(wire.CodeBadRequest, "mime missing or exceeds 255 bytes")
	}
	data, err := base64.StdEncoding.DecodeString(in.Base64)
	if err != nil {
		return wire.ArtifactPutResult{}, Errorf(wire.CodeBadRequest, "base64 decode failed: %v", err)
	}
	if len(data) == 0 || len(data) > MaxArtifactPut {
		return wire.ArtifactPutResult{}, invalid("artifact size %d bytes must be between 1 and %d", len(data), MaxArtifactPut)
	}
	h, err := art.Put(ctx, in.Session, in.MIME, data)
	if err != nil {
		return wire.ArtifactPutResult{}, err
	}
	return wire.ArtifactPutResult{Ref: toolhub.ArtifactPrefix + h}, nil
}

// Trace 渲染一次任务经过的会话、信封、工具与拒绝原因。
func (v *App) Trace(ctx context.Context, in wire.TraceArgs) (wire.TraceResult, error) {
	if in.Trace == "" {
		return wire.TraceResult{}, Errorf(wire.CodeBadRequest, "missing trace")
	}
	out, err := RenderTrace(ctx, v.o.DB, in.Trace)
	return wire.TraceResult{Text: out}, err
}

// —— 插件 ——

// PluginsList 全部已登记插件的开关状态、当前配置与配置 Schema；装配了 Profiles 时还给可写的层，
// 以及改过、重启后才生效的状态（pending）。
func (v *App) PluginsList(_ context.Context, _ wire.Empty) (wire.PluginsListResult, error) {
	if v.o.Profile == nil {
		return wire.PluginsListResult{}, Errorf(wire.CodeUnsupported, "plugin information not available")
	}
	var next *component.Profile
	out := wire.PluginsListResult{Plugins: []wire.PluginInfo{}, Scopes: []wire.ProfileScope{}}
	if pr := v.o.Profiles; pr != nil {
		out.Scopes = pr.Scopes()
		out.Denied = pr.Denied()
		next, _ = pr.Reload() // 文件读不出来就不报 pending（plugins.set 时会报错）
	}
	for _, st := range v.o.Profile.States() {
		out.Plugins = append(out.Plugins, v.pluginInfo(st, v.o.Profile, next))
	}
	return out, nil
}

// pluginInfo 一个插件的线上信息；next 非空时与它比较得出 pending。
func (v *App) pluginInfo(st component.State, cur, next *component.Profile) wire.PluginInfo {
	wp := wire.PluginInfo{Name: st.Name, On: st.On, Default: st.Default, Source: st.Source,
		Effects: append([]string{}, st.Effects...), Schema: []wire.ConfigField{}, Config: configOf(cur, st.Name),
		Mandatory: slices.Contains(cur.Mandatory, st.Name), Risks: PluginRisks(st.Effects)}
	if len(wp.Risks) == 0 {
		wp.Risks = nil // 没有风险时不发这个字段（omitempty）
	}
	if m, ok := component.MetaOf(st.Name); ok {
		wp.Version, wp.Description = m.Version, m.Description // 客户端改进 M10 T17
		for _, f := range component.SchemaOf(m.Config) {
			wp.Schema = append(wp.Schema, wire.ConfigField{Key: f.Key, Type: f.Type, Default: f.Default, Title: f.Title,
				Description: f.Description, Group: f.Group, Widget: f.Widget, Elem: f.Elem})
		}
	}
	if next != nil {
		on := st.On
		for _, ns := range next.States() {
			if ns.Name == st.Name {
				on = ns.On
			}
		}
		nc := configOf(next, st.Name)
		if on != wp.On || !jsonEqual(nc, wp.Config) {
			wp.Pending = &wire.PluginPending{On: on, Config: nc}
		}
	}
	return wp
}

// configOf profile 合并后某插件写到的配置（没有时为空对象）。
func configOf(p *component.Profile, name string) map[string]any {
	out := map[string]any{}
	for _, e := range p.Plugins {
		if e.Name == name {
			maps.Copy(out, e.Config)
		}
	}
	return out
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

// PluginsSet 改一层 profile 里某插件的开关与配置，按 if_revision 条件写，重启后生效。
// 校验：插件已登记、配置键与类型合 Schema、强制插件不能停用、项目层不能设 Denied 的键、项目未受信时项目层不可写。
// 写完按全部层重新合并一次，合并失败（如关掉了下层设成强制的插件）时还原文件并返回 invalid。
func (v *App) PluginsSet(_ context.Context, in wire.PluginsSetArgs) (wire.PluginsSetResult, error) {
	pr := v.o.Profiles
	if pr == nil || v.o.Profile == nil {
		return wire.PluginsSetResult{}, Errorf(wire.CodeUnsupported, "plugins.set not available: edit profile.toml and restart")
	}
	var scope *wire.ProfileScope
	for _, s := range pr.Scopes() {
		if s.Scope == in.Scope {
			scope = &s
		}
	}
	if scope == nil {
		return wire.PluginsSetResult{}, invalid("scope must be user or project, got %q", in.Scope)
	}
	if !scope.Writable {
		return wire.PluginsSetResult{}, Errorf(wire.CodeDenied, "%s", scope.Reason)
	}
	if _, ok := component.MetaOf(in.Name); !ok {
		return wire.PluginsSetResult{}, Errorf(wire.CodeNotFound, "plugin %s is not registered", in.Name)
	}
	if in.Enabled != nil && !*in.Enabled && slices.Contains(v.o.Profile.Mandatory, in.Name) {
		return wire.PluginsSetResult{}, invalid("%s is mandatory and cannot be disabled", in.Name)
	}
	if in.Scope == "project" {
		for _, k := range pr.Denied()[in.Name] {
			if _, ok := in.Config[k]; ok {
				return wire.PluginsSetResult{}, Errorf(wire.CodeDenied, "project profile cannot set %s.%s (endpoints, credentials and storage locations are user-level only)", in.Name, k)
			}
		}
	}
	set := map[string]any{}
	for k, val := range in.Config {
		if val != nil {
			set[k] = val
		}
	}
	set, err := component.CheckConfig(in.Name, set)
	if err != nil {
		return wire.PluginsSetResult{}, invalid("invalid config for %s: %v", in.Name, err)
	}
	edit := map[string]any{}
	for k, val := range in.Config {
		if val == nil {
			edit[k] = nil // 删除该键
		} else {
			edit[k] = set[k]
		}
	}
	backup, _ := os.ReadFile(scope.Path)
	existed := backup != nil
	rev, err := pr.Edit(in.Scope, component.PluginEdit{Name: in.Name, Enabled: in.Enabled, Config: edit}, in.IfRevision)
	if errors.Is(err, component.ErrRevision) {
		return wire.PluginsSetResult{}, conflict(wire.ConflictStale, "%s was modified elsewhere; refresh and retry", scope.Path)
	}
	if err != nil {
		return wire.PluginsSetResult{}, err
	}
	next, err := pr.Reload()
	if err != nil { // 合并不过：还原，免得下次启动失败
		if existed {
			_ = os.WriteFile(scope.Path, backup, 0o600)
		} else {
			_ = os.Remove(scope.Path)
		}
		return wire.PluginsSetResult{}, invalid("profile merge failed after the edit; reverted: %v", err)
	}
	out := wire.PluginsSetResult{Revision: rev, Restart: true}
	for _, st := range v.o.Profile.States() {
		if st.Name == in.Name {
			out.Plugin = v.pluginInfo(st, v.o.Profile, next)
		}
	}
	return out, nil
}

// MaxFiles workspace.files 单次最多返回的文件数。
const MaxFiles = 200

// WorkspaceFiles 列工作区文件（界面 @ 文件的候选）。没有装配时返回 unsupported。
func (v *App) WorkspaceFiles(ctx context.Context, in wire.FilesArgs) (wire.FilesResult, error) {
	if v.o.Files == nil {
		return wire.FilesResult{}, Errorf(wire.CodeUnsupported, "workspace file list not available")
	}
	limit := in.Limit
	if limit <= 0 || limit > MaxFiles {
		limit = 50
	}
	if len(in.Query) > 256 {
		return wire.FilesResult{}, invalid("query exceeds 256 bytes")
	}
	fs, trunc, err := v.o.Files(ctx, in.Query, limit)
	if err != nil {
		return wire.FilesResult{}, err
	}
	if fs == nil {
		fs = []wire.FileInfo{}
	}
	return wire.FilesResult{Files: fs, Truncated: trunc}, nil
}

// need 会话 id 必填。
func need(id string) error {
	if id == "" {
		return Errorf(wire.CodeBadRequest, "missing session")
	}
	return nil
}

// modes 权限档位；未装权限插件返回 unsupported。
func (a *App) modes() (Modes, error) {
	if a.o.Modes == nil {
		return nil, Errorf(wire.CodeUnsupported, "permission plugin not installed")
	}
	return a.o.Modes, nil
}
