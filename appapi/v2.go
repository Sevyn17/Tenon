package appapi

// protocol 2 命令（docs/06 - 应用协议契约.md 10.4）：参数与结果全部用 appapi/wire 的线上类型，错误码按 10.7。
// 与 protocol 1 的 Call 并存到 web/ 切换完成（10.11 S8），之后 protocol 1 删除、V2 的方法并回 App。
// 订阅（session.subscribe / unsubscribe）随连接状态在传输层实现（S4）；交互命令（interaction.respond、
// interactions.list）在 S6 加入。
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
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"ybase/appapi/wire"
	"ybase/runtime/agentdef"
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

// applyWait SetExt / SetConfig 最多同步等多久（见 V2.apply）。
var applyWait = 2 * time.Second

// metaClientID session.create 记在会话行业务键里的 client_id（同一 client_id 重发返回已建会话）。
const metaClientID = "client_id"

// V2 protocol 2 的命令集。用 App.V2() 取得；方法并发安全。
type V2 struct{ a *App }

// V2 protocol 2 命令集。
func (a *App) V2() V2 { return V2{a} }

type handler2 func(ctx context.Context, v V2, args json.RawMessage) (any, error)

// bind2 同 bind，错误按 protocol 2 归类（AsError2）。
func bind2[In, Out any](f func(V2, context.Context, In) (Out, error)) handler2 {
	return func(ctx context.Context, v V2, raw json.RawMessage) (any, error) {
		var in In
		if t := bytes.TrimSpace(raw); len(t) > 0 && !bytes.Equal(t, []byte("null")) {
			if err := json.Unmarshal(t, &in); err != nil {
				return nil, Errorf(wire.CodeBadRequest, "参数解析失败：%v", err)
			}
		}
		out, err := f(v, ctx, in)
		if err != nil {
			return nil, AsError2(err)
		}
		return out, nil
	}
}

var commands2 = map[string]handler2{
	"hello":            bind2(V2.Hello),
	"sessions.list":    bind2(V2.SessionsList),
	"session.create":   bind2(V2.Create),
	"session.get":      bind2(V2.Get),
	"session.entries":  bind2(V2.Entries),
	"session.send":     bind2(V2.Send),
	"session.steer":    bind2(V2.Steer),
	"session.cancel":   bind2(V2.Cancel),
	"session.rewind":   bind2(V2.Rewind),
	"session.compact":  bind2(V2.Compact),
	"session.fork":     bind2(V2.Fork),
	"session.stats":    bind2(V2.Stats),
	"session.settings": bind2(V2.Settings),
	"mode.list":        bind2(V2.ModeList),
	"mode.get":         bind2(V2.ModeGet),
	"mode.set":         bind2(V2.ModeSet),
	"mode.show":        bind2(V2.ModeShow),
	"artifact.get":     bind2(V2.ArtifactGet),
	"artifact.put":     bind2(V2.ArtifactPut),
	"trace.explain":    bind2(V2.Trace),
	"plugins.list": bind2(func(v V2, ctx context.Context, in NoArgs) ([]PluginInfo, error) {
		return v.a.PluginsList(ctx, in)
	}),
	"packages.list": bind2(func(v V2, ctx context.Context, in NoArgs) ([]PackageInfo, error) {
		return v.a.PackagesList(ctx, in)
	}),
	"plugins.set": bind2(func(v V2, ctx context.Context, in PluginsSetArgs) (Empty, error) {
		return v.a.PluginsSet(ctx, in)
	}),
}

// Commands2 protocol 2 支持的命令名（排序后）。
func Commands2() []string {
	out := make([]string, 0, len(commands2))
	for k := range commands2 {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Call2 按命令名分发 protocol 2 命令。错误一律是 *Error（码见 wire.Code*）。
func (a *App) Call2(ctx context.Context, cmd string, args json.RawMessage) (any, error) {
	h, ok := commands2[cmd]
	if !ok {
		return nil, Errorf(wire.CodeUnsupported, "未知命令 %q", cmd)
	}
	return h(ctx, a.V2(), args)
}

// AsError2 按 protocol 2 的错误码（10.7）归类。已是 *Error 的：带原错误的按原错误重新归类（protocol 1 的归类不同，
// 如 closed 在 protocol 1 里是 not_found，G2），不带的原样返回。
func AsError2(err error) *Error {
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
func (v V2) Hello(_ context.Context, in wire.HelloArgs) (wire.HelloResult, error) {
	lo, hi := in.Protocol.Min, in.Protocol.Max
	if lo == 0 && hi == 0 {
		lo, hi = wire.Protocol, wire.Protocol
	}
	if lo > wire.Protocol || hi < wire.Protocol {
		return wire.HelloResult{}, Errorf(wire.CodeUnsupported, "协议区间 [%d, %d] 不含服务端支持的 %d", lo, hi, wire.Protocol)
	}
	o := v.a.o
	return wire.HelloResult{Protocol: wire.Protocol, Server: Server, Principal: LocalPrincipal,
		Capabilities: v.capabilities(), Model: o.Model, Workdir: o.Workdir, Trusted: o.Trusted, MaxOutput: MaxWireText}, nil
}

// capabilities 按装配了哪些部件列出可选功能，界面据此决定显示哪些入口。
func (v V2) capabilities() []string {
	o := v.a.o
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
	return out
}

// SessionsList 按更新时间倒序分页列会话。
func (v V2) SessionsList(ctx context.Context, in wire.ListArgs) (wire.ListResult, error) {
	r, err := v.a.SessionsList(ctx, ListArgs{Cursor: in.Cursor, Limit: in.Limit, All: in.All})
	if err != nil {
		return wire.ListResult{}, err
	}
	out := wire.ListResult{Items: make([]wire.SessionInfo, len(r.Items)), Next: r.Next}
	for i, s := range r.Items {
		out.Items[i] = wire.SessionInfo{ID: s.ID, Title: s.Title, Preview: s.Preview, RunState: wireRun(s.RunState),
			Origin: s.Origin, Parent: s.ParentID, UpdatedAt: s.UpdatedAt}
	}
	return out, nil
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
func (v V2) Create(ctx context.Context, in wire.CreateArgs) (wire.SessionSnapshot, error) {
	if v.a.o.Host.New == nil {
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
	if _, err := v.a.o.Host.Create(ctx, id, in.Definition, meta); err != nil {
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
func (v V2) createdBy(ctx context.Context, id, clientID string) bool {
	l, err := host.LoadHead(ctx, v.a.o.DB, id)
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
func (v V2) Get(ctx context.Context, in wire.SessionArgs) (wire.SessionSnapshot, error) {
	if err := need(in.Session); err != nil {
		return wire.SessionSnapshot{}, err
	}
	return v.snapshot(ctx, in.Session)
}

// snapshot 会话头、运行状态与游标来自同一次读取（06 10.2.2 规则 4）：之后的变化一定在 cursor 之后。
// 未结束的交互在 S6 补上，live 在 S5 补上。
func (v V2) snapshot(ctx context.Context, id string) (wire.SessionSnapshot, error) {
	l, err := host.LoadHead(ctx, v.a.o.DB, id)
	if err != nil {
		return wire.SessionSnapshot{}, err
	}
	h := l.Snapshot.Header
	modeHdr := h // 档位持久化在顶层会话头上（子会话跟随顶层）
	if top := topOf(id); top != id {
		if tl, err := host.LoadHead(ctx, v.a.o.DB, top); err == nil {
			modeHdr = tl.Snapshot.Header
		}
	}
	return wire.SessionSnapshot{ID: id, Parent: l.Meta.ParentID, Origin: l.Meta.Origin, Title: l.Meta.Title,
		RunState: WireRunState(h.Status), Mode: v.committedMode(id, modeHdr), Settings: wire.Settings{MaxSteps: maxSteps(h)},
		Log: wire.LogPos{Base: h.CompactionBase, Len: h.LogLen}, Interactions: []wire.Interaction{}, Cursor: l.Cursor}, nil
}

// committedMode 快照里的档位：已提交值（06 10.2.2 规则 5）。内存里已切换、还没落库时给落库的那个，
// 标签从档位表取（沙箱级别只有当前档位才有，这时留空，客户端以随后的 mode_changed 为准）。
func (v V2) committedMode(id string, h session.Header) *wire.Mode {
	m := v.a.o.Modes
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
func (v V2) Entries(ctx context.Context, in wire.EntriesArgs) (wire.EntriesResult, error) {
	r, err := v.a.Entries(ctx, EntriesArgs{Session: in.Session, From: in.From, To: in.To, Tail: in.Tail})
	if err != nil {
		return wire.EntriesResult{}, err
	}
	out := wire.EntriesResult{Entries: make([]wire.Entry, len(r.Entries)), Base: r.Base, Len: r.LogLen}
	spill := v.spill(in.Session)
	for i, e := range r.Entries {
		out.Entries[i] = WireEntry(e.Idx, e.Entry, spill)
	}
	return out, nil
}

// spill 把超长工具结果全文存成工件（内容寻址，重复存无害）。
func (v V2) spill(id string) Spill {
	art := v.a.o.Artifacts
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
func (v V2) Send(ctx context.Context, in wire.SendArgs) (wire.SendResult, error) {
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
	hd, err := v.a.o.Host.OpenExisting(ctx, in.Session)
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
func (v V2) content(parts []wire.Part, text string) (session.Content, error) {
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
			art := v.a.o.Artifacts
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
func (v V2) Steer(ctx context.Context, in wire.SteerArgs) (wire.Empty, error) {
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
func (v V2) Cancel(ctx context.Context, in wire.SessionArgs) (wire.Empty, error) {
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
func (v V2) running(ctx context.Context, id string) (host.Handle, error) {
	if hd, ok := v.a.o.Host.Get(id); ok {
		return hd, nil
	}
	ok, err := v.a.o.DB.Exists(ctx, id)
	if err != nil {
		return host.Handle{}, err
	}
	if !ok {
		return host.Handle{}, fmt.Errorf("%w: %s", host.ErrSessionNotFound, id)
	}
	return host.Handle{}, session.ErrNotWaiting
}

// idleHandle 打开已有会话，要求没有进行中的回合（需空闲的命令用）。
func (v V2) idleHandle(ctx context.Context, id string) (host.Handle, error) {
	hd, err := v.a.o.Host.OpenExisting(ctx, id)
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
func (v V2) Rewind(ctx context.Context, in wire.RewindArgs) (wire.RewindResult, error) {
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
func (v V2) Compact(ctx context.Context, in wire.CompactArgs) (wire.CompactResult, error) {
	if err := need(in.Session); err != nil {
		return wire.CompactResult{}, err
	}
	if v.a.o.Compactor == nil {
		return wire.CompactResult{}, Errorf(wire.CodeUnsupported, "未装配 compact-basic 插件")
	}
	hd, err := v.idleHandle(ctx, in.Session)
	if err != nil {
		return wire.CompactResult{}, err
	}
	l, err := v.a.o.DB.Load(ctx, in.Session, nil)
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
	c := v.a.o.Compactor
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
func (v V2) Fork(ctx context.Context, in wire.ForkArgs) (wire.SessionSnapshot, error) {
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
	if hd, ok := v.a.o.Host.Get(in.Session); ok && hd.InTurn() {
		return wire.SessionSnapshot{}, fmt.Errorf("%w: 来源会话 %s 有进行中的回合", session.ErrNotIdle, in.Session)
	}
	upto := -1
	if in.Upto != nil {
		upto = *in.Upto
	}
	if _, err := v.a.o.Host.ForkIf(ctx, in.Session, to, upto, in.Origin, in.IfCursor); err != nil {
		if !errors.Is(err, store.ErrConflict) {
			return wire.SessionSnapshot{}, err
		}
		if in.ClientID != "" {
			if l, e := host.LoadHead(ctx, v.a.o.DB, to); e == nil && l.Meta.ParentID == in.Session {
				return v.snapshot(ctx, to)
			}
		}
		return wire.SessionSnapshot{}, conflict(wire.ConflictExists, "会话 %s 已存在", to)
	}
	return v.snapshot(ctx, to)
}

// —— 查询与设置 ——

// exists 会话存在，否则 not_found。
func (v V2) exists(ctx context.Context, id string) error {
	if err := need(id); err != nil {
		return err
	}
	ok, err := v.a.o.DB.Exists(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s", host.ErrSessionNotFound, id)
	}
	return nil
}

// Stats 会话统计（只读投影）。
func (v V2) Stats(ctx context.Context, in wire.SessionArgs) (wire.Stats, error) {
	if err := v.exists(ctx, in.Session); err != nil {
		return wire.Stats{}, err
	}
	st, err := v.a.Stats(ctx, SessionArgs{Session: in.Session})
	if err != nil {
		return wire.Stats{}, err
	}
	out := wire.Stats{Attempts: st.Attempts, Reported: st.Reported, Missing: st.Missing, Errors: st.Errors,
		Input: st.Input, Output: st.Output, CacheRead: st.CacheRead, CacheWrite: st.CacheWrite, HitRate: st.HitRate,
		ToolCalls: st.ToolCalls, Compacts: st.Compacts, LastRequest: st.LastRequest}
	if st.Composition != nil {
		out.Composition, _ = json.Marshal(st.Composition)
	}
	return out, nil
}

// Settings 查看或修改会话设置（取代 session.max_steps）。空闲时等落库后返回 committed=true；
// 回合中只改内存、到步边界生效，返回 committed=false，落库时发 session.settings_changed（10.2.2 规则 5）。
func (v V2) Settings(ctx context.Context, in wire.SettingsArgs) (wire.SettingsResult, error) {
	if err := need(in.Session); err != nil {
		return wire.SettingsResult{}, err
	}
	if in.MaxSteps == nil {
		l, err := host.LoadHead(ctx, v.a.o.DB, in.Session)
		if err != nil {
			return wire.SettingsResult{}, err
		}
		return wire.SettingsResult{Settings: wire.Settings{MaxSteps: maxSteps(l.Snapshot.Header)}, Committed: true}, nil
	}
	hd, err := v.a.o.Host.OpenExisting(ctx, in.Session)
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
func (v V2) apply(id string, hd host.Handle, c host.Command) (bool, error) {
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
				v.a.Notice(id, fmt.Sprintf("[设置未能保存：%v]", err))
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
func (v V2) ModeList(_ context.Context, _ wire.Empty) (wire.ModeListResult, error) {
	m, err := v.a.modes()
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
func (v V2) ModeGet(ctx context.Context, in wire.SessionArgs) (wire.ModeSetResult, error) {
	m, err := v.a.modes()
	if err != nil {
		return wire.ModeSetResult{}, err
	}
	if err := v.exists(ctx, in.Session); err != nil {
		return wire.ModeSetResult{}, err
	}
	st := m.Get(in.Session)
	committed := true
	if l, err := host.LoadHead(ctx, v.a.o.DB, topOf(in.Session)); err == nil {
		key, _ := m.ExtKeys()
		if saved, ok := l.Snapshot.Header.Ext[key].(string); ok && saved != st.Mode {
			committed = false
		}
	}
	return wire.ModeSetResult{State: wireMode(st), Committed: committed}, nil
}

// ModeSet 切换档位：立即对后续工具调用生效，持久化到顶层会话头。空闲时等落库，committed=true；
// 回合中到步边界落库，committed=false，落库时发 session.mode_changed（10.2.2 规则 5）。
func (v V2) ModeSet(ctx context.Context, in wire.ModeSetArgs) (wire.ModeSetResult, error) {
	m, err := v.a.modes()
	if err != nil {
		return wire.ModeSetResult{}, err
	}
	if in.Mode == "" {
		return wire.ModeSetResult{}, Errorf(wire.CodeBadRequest, "缺少 mode")
	}
	if err := v.exists(ctx, in.Session); err != nil { // 先查会话，再改内存档位
		return wire.ModeSetResult{}, err
	}
	top, err := v.a.o.Host.OpenExisting(ctx, topOf(in.Session))
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
func (v V2) ModeShow(ctx context.Context, in wire.ModeShowArgs) (wire.ModeShowResult, error) {
	m, err := v.a.modes()
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
func (v V2) ArtifactGet(ctx context.Context, in wire.ArtifactGetArgs) (wire.ArtifactGetResult, error) {
	art := v.a.o.Artifacts
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
func (v V2) ArtifactPut(ctx context.Context, in wire.ArtifactPutArgs) (wire.ArtifactPutResult, error) {
	art := v.a.o.Artifacts
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
func (v V2) Trace(ctx context.Context, in wire.TraceArgs) (wire.TraceResult, error) {
	r, err := v.a.ExplainTrace(ctx, TraceArgs{Trace: in.Trace})
	return wire.TraceResult{Text: r.Text}, err
}
