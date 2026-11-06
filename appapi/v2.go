package appapi

// 应用协议命令（protocol 2，docs/06 - 应用协议契约.md 10.4）：参数与结果全部用 appapi/wire 的线上类型，错误码按 10.7。
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
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"ybase/appapi/wire"
	"ybase/runtime/agentdef"
	"ybase/runtime/bus"
	"ybase/runtime/component"
	"ybase/runtime/host"
	"ybase/runtime/reqlog"
	"ybase/runtime/session"
	"ybase/runtime/store"
	"ybase/runtime/toolhub"
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
				return nil, Errorf(wire.CodeBadRequest, "参数解析失败：%v", err)
			}
		}
		out, err := f(a, ctx, in)
		if err != nil {
			return nil, AsError(err)
		}
		return out, nil
	}
}

var commands = map[string]handler{
	"hello":               bind((*App).Hello),
	"sessions.list":       bind((*App).SessionsList),
	"session.create":      bind((*App).Create),
	"session.update":      bind((*App).Update),
	"session.delete":      bind((*App).Delete),
	"session.get":         bind((*App).Get),
	"session.entries":     bind((*App).Entries),
	"session.send":        bind((*App).Send),
	"session.steer":       bind((*App).Steer),
	"session.cancel":      bind((*App).Cancel),
	"session.rewind":      bind((*App).Rewind),
	"session.compact":     bind((*App).Compact),
	"session.fork":        bind((*App).Fork),
	"session.stats":       bind((*App).Stats),
	"session.settings":    bind((*App).Settings),
	"mode.list":           bind((*App).ModeList),
	"mode.get":            bind((*App).ModeGet),
	"mode.set":            bind((*App).ModeSet),
	"mode.show":           bind((*App).ModeShow),
	"artifact.get":        bind((*App).ArtifactGet),
	"artifact.put":        bind((*App).ArtifactPut),
	"trace.explain":       bind((*App).Trace),
	"interaction.respond": bind((*App).Respond),
	"interactions.list":   bind((*App).Interactions),
	"plugins.list":        bind((*App).PluginsList),
	"packages.list":       bind((*App).PackagesList),
	"plugins.set":         bind((*App).PluginsSet),
	"workspace.files":     bind((*App).WorkspaceFiles),
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
		return nil, Errorf(wire.CodeUnsupported, "未知命令 %q", cmd)
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
		return wire.HelloResult{}, Errorf(wire.CodeUnsupported, "协议区间 [%d, %d] 不含服务端支持的 %d", lo, hi, wire.Protocol)
	}
	o := v.o
	return wire.HelloResult{Protocol: wire.Protocol, Server: Server, Principal: LocalPrincipal,
		Capabilities: v.capabilities(), Model: o.Model, Workdir: o.Workdir, Trusted: o.Trusted, MaxOutput: MaxWireText}, nil
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
	}
	if o.Profile != nil {
		out = append(out, "plugins")
	}
	if o.Files != nil {
		out = append(out, "files")
	}
	return out
}

// SessionsList 按更新时间倒序分页列会话。
func (v *App) SessionsList(ctx context.Context, in wire.ListArgs) (wire.ListResult, error) {
	limit := in.Limit
	if limit <= 0 || limit > 500 {
		limit = 50
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
			if in.All || s.ParentID == "" {
				out.Items = append(out.Items, wireInfo(s))
			}
		}
		return out, nil
	}
	items, err := v.o.DB.ListSessions(ctx, in.Cursor, limit, !in.All)
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

func wireInfo(s store.SessionInfo) wire.SessionInfo {
	return wire.SessionInfo{ID: s.ID, Title: s.Title, Preview: s.Preview, RunState: wireRun(s.RunState),
		Origin: s.Origin, Parent: s.ParentID, UpdatedAt: s.UpdatedAt, Pinned: pinnedOf(s.Meta)}
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
		return wire.SessionSnapshot{}, Errorf(wire.CodeUnsupported, "存储不支持改会话元数据")
	}
	var title *string
	if in.Title != nil {
		t := strings.TrimSpace(*in.Title)
		if len(t) > MaxTitle {
			return wire.SessionSnapshot{}, invalid("标题超过 %d 字节", MaxTitle)
		}
		for _, r := range t {
			if r < 0x20 || r == 0x7f {
				return wire.SessionSnapshot{}, invalid("标题不能含换行或控制字符")
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
	if title == nil && len(set) == 0 {
		return v.snapshot(ctx, in.Session)
	}
	if err := ms.UpdateSessionMeta(ctx, in.Session, title, set); err != nil {
		return wire.SessionSnapshot{}, err
	}
	return v.snapshot(ctx, in.Session)
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
		return wire.Empty{}, invalid("子会话随顶层会话一起删除")
	}
	ids, err := v.tree(ctx, in.Session)
	if err != nil {
		return wire.Empty{}, err
	}
	for _, id := range ids {
		if hd, ok := v.o.Host.Get(id); ok && hd.InTurn() {
			return wire.Empty{}, fmt.Errorf("%w: 会话 %s 有进行中的回合", session.ErrNotIdle, id)
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
		return wire.SessionSnapshot{}, Errorf(wire.CodeInternal, "appapi: 未设置会话构造器")
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
	var meta json.RawMessage
	if in.ClientID != "" {
		meta, _ = json.Marshal(map[string]string{metaClientID: in.ClientID})
	}
	if _, err := v.o.Host.Create(ctx, id, in.Definition, meta); err != nil {
		if !errors.Is(err, store.ErrConflict) {
			return wire.SessionSnapshot{}, err
		}
		if in.ClientID != "" && v.createdBy(ctx, id, in.ClientID) {
			return v.snapshot(ctx, id)
		}
		return wire.SessionSnapshot{}, conflict(wire.ConflictExists, "会话 %s 已存在", id)
	}
	return v.snapshot(ctx, id)
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
		return invalid("会话 id %q 不合法：不能含 / 或 \\，不超过 128 字节", id)
	}
	for _, r := range id {
		if r < 0x20 || r == 0x7f {
			return invalid("会话 id 不能含控制字符")
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
	return wire.SessionSnapshot{ID: id, Parent: l.Meta.ParentID, Origin: l.Meta.Origin, Title: l.Meta.Title, Pinned: pinnedOf(l.Meta.Meta),
		RunState: WireRunState(h.Status), Mode: v.committedMode(id, modeHdr), Settings: wire.Settings{MaxSteps: maxSteps(h)},
		Log: wire.LogPos{Base: h.CompactionBase, Len: h.LogLen}, Interactions: v.openInteractions(ctx, id), Cursor: l.Cursor,
		Live: v.live(id, h)}, nil
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

func maxSteps(h session.Header) uint32 {
	if h.Config == nil {
		return 0
	}
	return h.Config.Policy.MaxSteps
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
		return wire.EntriesResult{}, invalid("区间 [%d, %d) 不合法", from, to)
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
	if err := hd.Do(host.Command{Kind: host.Send, ItemID: in.ClientID, Content: content}); err != nil {
		return wire.SendResult{}, err
	}
	return wire.SendResult{ClientID: in.ClientID, Queued: queued}, nil
}

func needClientID(id string) error {
	if id == "" {
		return Errorf(wire.CodeBadRequest, "缺少 client_id")
	}
	if len(id) > 128 {
		return invalid("client_id 超过 128 字节")
	}
	return nil
}

// content 输入部件 → 内核内容。只收文本与媒体；媒体必须引用本服务的工件（先 artifact.put 再引用）。
func (v *App) content(parts []wire.Part, text string) (session.Content, error) {
	if (len(parts) > 0) == (text != "") {
		return nil, Errorf(wire.CodeBadRequest, "content 与 text 必须给且只给一个")
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
				return nil, invalid("第 %d 个部件：media 须带 ref 与 mime", i+1)
			}
			hash, ok := artifactHash(m.Ref)
			if !ok {
				return nil, invalid("第 %d 个部件：ref 须是 %s<sha256>", i+1, toolhub.ArtifactPrefix)
			}
			art := v.o.Artifacts
			if art == nil { // 没有工件存储就核对不了引用，也取不回内容
				return nil, Errorf(wire.CodeUnsupported, "没有工件存储，不能发送媒体")
			}
			data, err := art.Get(hash)
			if err != nil {
				return nil, invalid("第 %d 个部件：工件 %s 不存在", i+1, m.Ref)
			}
			// 大小以实际内容为准，不信客户端报的；引用统一成小写哈希。
			out = append(out, session.Part{Kind: "media", Media: &session.MediaRef{URI: toolhub.ArtifactPrefix + hash,
				MIME: m.MIME, Bytes: uint64(len(data)), SHA256: hash}})
		default:
			return nil, invalid("第 %d 个部件：不支持的种类 %q（只收 text / media）", i+1, p.Kind)
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
		return wire.Empty{}, Errorf(wire.CodeBadRequest, "缺少 text")
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
		return host.Handle{}, fmt.Errorf("%w: 会话 %s 有进行中的回合", session.ErrNotIdle, id)
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
		return wire.RewindResult{}, invalid("to_turn 须 ≥ 1")
	}
	hd, err := v.idleHandle(ctx, in.Session)
	if err != nil {
		return wire.RewindResult{}, err
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
		return wire.CompactResult{}, Errorf(wire.CodeUnsupported, "未装配 compact-basic 插件")
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
		return wire.CompactResult{}, fmt.Errorf("%w: 期望 %d，当前 %d", host.ErrStale, *in.IfCursor, l.Cursor)
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
			return wire.CompactResult{}, invalid("upto=%d 不是合法的压缩点（不能拆开工具调用与结果，不能早于当前压缩点 %d）", *in.Upto, h.CompactionBase)
		}
		view, meta, err = c.At(ctx, in.Session, core, *in.Upto)
	} else if view, meta, err = c.Plan(ctx, in.Session, core, true); err == nil && view == nil {
		view, meta, err = c.At(ctx, in.Session, core, h.LogLen)
	}
	if err != nil {
		return wire.CompactResult{}, err
	}
	if view == nil {
		return wire.CompactResult{}, invalid("没有可压缩的内容")
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
		return wire.SessionSnapshot{}, invalid("origin 只能是 fork / side，得到 %q", in.Origin)
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
		return wire.SessionSnapshot{}, fmt.Errorf("%w: 来源会话 %s 有进行中的回合", session.ErrNotIdle, in.Session)
	}
	upto := -1
	if in.Upto != nil {
		upto = *in.Upto
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
		return wire.SessionSnapshot{}, conflict(wire.ConflictExists, "会话 %s 已存在", to)
	}
	return v.snapshot(ctx, to)
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
	if in.MaxSteps == nil {
		l, err := host.LoadHead(ctx, v.o.DB, in.Session)
		if err != nil {
			return wire.SettingsResult{}, err
		}
		return wire.SettingsResult{Settings: wire.Settings{MaxSteps: maxSteps(l.Snapshot.Header)}, Committed: true}, nil
	}
	hd, err := v.o.Host.OpenExisting(ctx, in.Session)
	if err != nil {
		return wire.SettingsResult{}, err
	}
	ok, err := v.apply(in.Session, hd, host.Command{Kind: host.SetConfig, MaxSteps: in.MaxSteps})
	if err != nil {
		return wire.SettingsResult{}, err
	}
	return wire.SettingsResult{Settings: wire.Settings{MaxSteps: *in.MaxSteps}, Committed: ok}, nil
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
				v.Notice(id, fmt.Sprintf("[设置未能保存：%v]", err))
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
		out.Modes = append(out.Modes, wire.ModeInfo{ID: d.ID, Label: d.Label, Builtin: d.Builtin, Project: d.Project, Widened: d.Widened})
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
		return wire.ModeSetResult{}, Errorf(wire.CodeBadRequest, "缺少 mode")
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
		return wire.ModeShowResult{}, Errorf(wire.CodeBadRequest, "缺少 session 或 mode")
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
		out.Items = append(out.Items, wire.ModeItem{Item: it.Item, Title: it.Title, Value: it.Value, Source: it.Source, Bounded: it.Bounded})
	}
	return out, nil
}

// —— 工件与 trace ——

// ArtifactGet 分段读工件（G10）：offset / length 按字节，单次最多 MaxArtifactChunk。文本类且这一段是完整 UTF-8 时给 text，
// 否则给 base64。
func (v *App) ArtifactGet(ctx context.Context, in wire.ArtifactGetArgs) (wire.ArtifactGetResult, error) {
	art := v.o.Artifacts
	if art == nil {
		return wire.ArtifactGetResult{}, Errorf(wire.CodeUnsupported, "没有工件存储")
	}
	if err := v.exists(ctx, in.Session); err != nil {
		return wire.ArtifactGetResult{}, err
	}
	hash, ok := artifactHash(in.Ref)
	if !ok {
		return wire.ArtifactGetResult{}, invalid("ref 须是 %s<sha256>", toolhub.ArtifactPrefix)
	}
	data, err := art.Get(hash)
	if err != nil {
		return wire.ArtifactGetResult{}, Errorf(wire.CodeNotFound, "工件 %s 不存在", in.Ref)
	}
	if in.Offset < 0 || in.Length < 0 || in.Offset > len(data) {
		return wire.ArtifactGetResult{}, invalid("区间不合法：offset=%d length=%d，工件共 %d 字节", in.Offset, in.Length, len(data))
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
		return wire.ArtifactPutResult{}, Errorf(wire.CodeUnsupported, "没有工件存储")
	}
	if err := v.exists(ctx, in.Session); err != nil {
		return wire.ArtifactPutResult{}, err
	}
	if in.MIME == "" || len(in.MIME) > 255 {
		return wire.ArtifactPutResult{}, Errorf(wire.CodeBadRequest, "缺少 mime 或超过 255 字节")
	}
	data, err := base64.StdEncoding.DecodeString(in.Base64)
	if err != nil {
		return wire.ArtifactPutResult{}, Errorf(wire.CodeBadRequest, "base64 解码失败：%v", err)
	}
	if len(data) == 0 || len(data) > MaxArtifactPut {
		return wire.ArtifactPutResult{}, invalid("工件大小 %d 字节，须在 1 到 %d 之间", len(data), MaxArtifactPut)
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
		return wire.TraceResult{}, Errorf(wire.CodeBadRequest, "缺少 trace")
	}
	out, err := RenderTrace(ctx, v.o.DB, in.Trace)
	return wire.TraceResult{Text: out}, err
}

// —— 插件 ——

// PluginsList 全部已登记插件的开关状态、当前配置与配置 Schema；装配了 Profiles 时还给可写的层，
// 以及改过、重启后才生效的状态（pending）。
func (v *App) PluginsList(_ context.Context, _ wire.Empty) (wire.PluginsListResult, error) {
	if v.o.Profile == nil {
		return wire.PluginsListResult{}, Errorf(wire.CodeUnsupported, "未提供插件信息")
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
		Mandatory: slices.Contains(cur.Mandatory, st.Name)}
	if m, ok := component.MetaOf(st.Name); ok {
		for _, f := range component.SchemaOf(m.Config) {
			wp.Schema = append(wp.Schema, wire.ConfigField{Key: f.Key, Type: f.Type, Default: f.Default, Title: f.Title,
				Description: f.Description, Group: f.Group, Widget: f.Widget})
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
		return wire.PluginsSetResult{}, Errorf(wire.CodeUnsupported, "plugins.set 未提供：请编辑 profile.toml 后重启")
	}
	var scope *wire.ProfileScope
	for _, s := range pr.Scopes() {
		if s.Scope == in.Scope {
			scope = &s
		}
	}
	if scope == nil {
		return wire.PluginsSetResult{}, invalid("scope 只能是 user / project，得到 %q", in.Scope)
	}
	if !scope.Writable {
		return wire.PluginsSetResult{}, Errorf(wire.CodeDenied, "%s", scope.Reason)
	}
	if _, ok := component.MetaOf(in.Name); !ok {
		return wire.PluginsSetResult{}, Errorf(wire.CodeNotFound, "插件 %s 未登记", in.Name)
	}
	if in.Enabled != nil && !*in.Enabled && slices.Contains(v.o.Profile.Mandatory, in.Name) {
		return wire.PluginsSetResult{}, invalid("%s 是强制插件，不能停用", in.Name)
	}
	if in.Scope == "project" {
		for _, k := range pr.Denied()[in.Name] {
			if _, ok := in.Config[k]; ok {
				return wire.PluginsSetResult{}, Errorf(wire.CodeDenied, "项目 profile 不能设 %s.%s（端点、凭据、存储位置只能写在用户级）", in.Name, k)
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
		return wire.PluginsSetResult{}, invalid("%s 的配置不合法：%v", in.Name, err)
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
		return wire.PluginsSetResult{}, conflict(wire.ConflictStale, "%s 已被别处修改，请刷新后再改", scope.Path)
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
		return wire.PluginsSetResult{}, invalid("改完后 profile 合并失败，已还原：%v", err)
	}
	out := wire.PluginsSetResult{Revision: rev, Restart: true}
	for _, st := range v.o.Profile.States() {
		if st.Name == in.Name {
			out.Plugin = v.pluginInfo(st, v.o.Profile, next)
		}
	}
	return out, nil
}

// PackagesList 已加载的插件包（用户级在前、项目级在后）。
func (v *App) PackagesList(_ context.Context, _ wire.Empty) (wire.PackagesListResult, error) {
	out := wire.PackagesListResult{Packages: []wire.PackageInfo{}}
	for _, k := range v.o.Packages {
		out.Packages = append(out.Packages, wire.PackageInfo{Name: k.Name, Version: k.Version, Description: k.Description,
			Scope: k.Scope, Source: k.File, Contents: k.Contents()})
	}
	return out, nil
}

// MaxFiles workspace.files 单次最多返回的文件数。
const MaxFiles = 200

// WorkspaceFiles 列工作区文件（界面 @ 文件的候选）。没有装配时返回 unsupported。
func (v *App) WorkspaceFiles(ctx context.Context, in wire.FilesArgs) (wire.FilesResult, error) {
	if v.o.Files == nil {
		return wire.FilesResult{}, Errorf(wire.CodeUnsupported, "未提供工作区文件列表")
	}
	limit := in.Limit
	if limit <= 0 || limit > MaxFiles {
		limit = 50
	}
	if len(in.Query) > 256 {
		return wire.FilesResult{}, invalid("query 超过 256 字节")
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
		return Errorf(wire.CodeBadRequest, "缺少 session")
	}
	return nil
}

// modes 权限档位；未装权限插件返回 unsupported。
func (a *App) modes() (Modes, error) {
	if a.o.Modes == nil {
		return nil, Errorf(wire.CodeUnsupported, "未装权限插件")
	}
	return a.o.Modes, nil
}
