// Package memstore：内存版 store.Store（T1a，测试与 tenontest 用）。
//
// 语义以 runtime/store/sqlite 为准，共用 runtime/store/storetest 一致性测试：
// 头部条件写、截断入归档、会话级单调 sync_seq、inbox 消费标记、会话键唯一、
// 工具记录状态机、定义版本 + 哈希、提交后通知。
// 条目、头部、外壳一律按 JSON 存取（与 sqlite 一样不与调用方共享指针）。
// 一把互斥锁保护全部数据，Commit 先校验再写，失败不留半截状态。
package memstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"tenon/runtime/bus"
	"tenon/runtime/session"
	"tenon/runtime/store"
)

// 编译期断言：*Store 实现 store.Store。
var _ store.Store = (*Store)(nil)

// ErrClosed：已关闭后再调用。
var ErrClosed = errors.New("memstore: 已关闭")

// Store 内存存储。零值不可用，用 New 创建。
type Store struct {
	mu       sync.Mutex
	txMu     sync.Mutex // Tx 之间串行（见 Tx）
	closed   bool
	clock    int64 // 单调时间戳（毫秒）：保证 updated_at / created_at 严格递增，排序稳定
	sessions map[string]*sess
	shells   map[string][]byte
	defs     map[string][]def // kind + "\x00" + id → 版本列表（下标 = 版本 - 1）
	// inbox 与工具记录按会话 id 存，不要求会话行存在（与 sqlite 无外键一致）。
	inbox   map[string][]inboxRow          // 会话 → 准入顺序
	tools   map[string]map[string]*toolRow // 会话 → call_id → 记录
	pending map[string]store.Pending
	outbox  []outboxRow        // 发件箱（08 9.6）：派发后保留行（sent），与 sqlite 一致
	traces  []store.TraceEvent // trace 事件（08 8.7 explain）

	cbMu     sync.Mutex
	onCommit func(bus.Envelope)
}

type sess struct {
	meta      store.SessionMeta
	header    []byte // 不含 Config
	shellHash string
	logLen    int
	nextSeq   int
	runState  string
	status    string // active / deleted
	preview   string
	updatedAt int64

	// 写租约（02 2.2）：holder 为空 = 无主；epoch 每抢一次加一。
	leaseHolder string
	leaseEpoch  int64

	entries  map[int]entry // idx → 条目
	archive  []archived
	requests map[string]request
	reqOrder []string
	spans    []spanRow

	// 会话事件序列（06 10.2.1，与 sqlite 的 session_events 同语义）：只追加，Replay 只读它；
	// eventFloor 是按保留期清掉的最大游标，早于它的回放返回 ErrCursorExpired。
	events     []evRow
	eventFloor int64
}

// evRow 一条会话事件。entry 带条目正文（条目写入后不变，与 sqlite 按 id 取回等价），rewind 带截断点，其余带原始 data。
type evRow struct {
	cursor int64
	kind   string
	idx    int
	to     int
	body   []byte
	data   json.RawMessage
	at     int64
}

type entry struct {
	body []byte
	at   int64
	seq  int
}

type archived struct {
	idx         int
	body        []byte
	seq         int // 原 sync_seq
	truncatedAt int64
	rewindSeq   int
}

type request struct {
	r         store.RequestRecord
	createdAt int64
}

// outboxRow 一条外发信封及其派发状态（与 sqlite 的 sent / claimed_by / claim_until / dead 同语义）。
type outboxRow struct {
	store.OutboxItem
	sent       bool
	claimedBy  string
	claimUntil int64
	dead       bool
	deadReason string
}

type inboxRow struct {
	it       store.InboxItem
	consumed bool
}

type toolRow struct {
	r       store.ToolRecord
	created int64
}

type spanRow struct {
	s   store.Span
	sid string
}

type def struct {
	body, hash string
	at         int64
}

// New 创建空存储。
func New() *Store {
	return &Store{sessions: map[string]*sess{}, shells: map[string][]byte{}, defs: map[string][]def{},
		inbox: map[string][]inboxRow{}, tools: map[string]map[string]*toolRow{}, pending: map[string]store.Pending{}, outbox: nil}
}

// Close 关闭；之后的调用返回 ErrClosed。
func (s *Store) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}

// now 取严格递增的毫秒时间（调用方持锁）。
func (s *Store) now() int64 {
	t := time.Now().UnixMilli()
	if t <= s.clock {
		t = s.clock + 1
	}
	s.clock = t
	return t
}

func (s *Store) lock() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	return nil
}

// live 取未删除会话（调用方持锁）。
func (s *Store) live(id string) *sess {
	if x := s.sessions[id]; x != nil && x.status != "deleted" {
		return x
	}
	return nil
}

// —— 会话 ——//

// splitHeader 与 sqlite 一致：Config 单独按外壳存，头部去掉 Config 后序列化。
func splitHeader(h session.Header) (hash string, shell, hb []byte, err error) {
	shell, err = json.Marshal(h.Config)
	if err != nil {
		return "", nil, nil, err
	}
	h.Config = nil
	hb, err = session.MarshalHeader(&h)
	if err != nil {
		return "", nil, nil, err
	}
	return store.HashBytes(shell), shell, hb, nil
}

func (s *Store) putShell(hash string, body []byte) {
	if _, ok := s.shells[hash]; !ok {
		s.shells[hash] = append([]byte(nil), body...)
	}
}

func cloneRaw(b json.RawMessage) json.RawMessage {
	if len(b) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), b...)
}

// CreateSession 新建会话。同 id 或同（定义, 创建者, 会话键）已存在返回错误。
func (s *Store) CreateSession(ctx context.Context, m store.SessionMeta, hdr session.Header, entries []*session.Entry) error {
	if err := s.lock(); err != nil {
		return err
	}
	defer s.mu.Unlock()
	if m.Origin == "" {
		m.Origin = "new"
	}
	if _, ok := s.sessions[m.ID]; ok { // 墓碑也占着 id（与 sqlite 主键一致）
		return fmt.Errorf("%w: 会话 %s 已存在", store.ErrConflict, m.ID)
	}
	if m.SessionKey != "" {
		for _, x := range s.sessions { // 唯一约束不看状态（与 sqlite 部分唯一索引一致）
			if x.meta.SessionKey == m.SessionKey && x.meta.AgentID == m.AgentID && x.meta.Creator == m.Creator {
				return fmt.Errorf("%w: 会话键（%s, %s, %s）已被 %s 占用", store.ErrConflict, m.AgentID, m.Creator, m.SessionKey, x.meta.ID)
			}
		}
	}
	hash, shell, hb, err := splitHeader(hdr)
	if err != nil {
		return err
	}
	bodies := make([][]byte, len(entries))
	for i, e := range entries {
		if bodies[i], err = json.Marshal(e); err != nil {
			return err
		}
	}
	if m.Workspace == "" && m.ParentID != "" { // 子会话跟随父会话的工作区（与 sqlite 一致）
		if p, ok := s.sessions[m.ParentID]; ok {
			m.Workspace = p.meta.Workspace
		}
	}
	m.Meta, m.UseConfig = cloneRaw(m.Meta), cloneRaw(m.UseConfig)
	x := &sess{meta: m, header: hb, shellHash: hash, nextSeq: 1, runState: "idle", status: "active",
		updatedAt: s.now(), entries: map[int]entry{}, requests: map[string]request{}}
	for i, e := range entries {
		x.entries[i] = entry{body: bodies[i], at: e.At, seq: x.nextSeq}
		x.events = append(x.events, evRow{cursor: int64(x.nextSeq), kind: store.EventEntry, idx: i, body: bodies[i], at: e.At})
		x.nextSeq++
	}
	x.logLen = len(entries)
	var evs []bus.Envelope
	// 分叉 / 旁支会话：forked 与建会话一起写（与 sqlite 一致，06 10.5 表一）。
	if m.Origin == "fork" || m.Origin == "side" {
		d, _ := json.Marshal(map[string]any{"from": m.ParentID, "upto": m.ForkUpto, "origin": m.Origin})
		at := s.now()
		x.events = append(x.events, evRow{cursor: int64(x.nextSeq), kind: store.EventForked, data: d, at: at})
		evs = append(evs, bus.Envelope{V: bus.Version, Seq: int64(x.nextSeq), At: at, Session: m.ID, Kind: store.EventForked,
			Data: json.RawMessage(append([]byte(nil), d...))})
		x.nextSeq++
	}
	s.putShell(hash, shell)
	s.sessions[m.ID] = x
	if len(evs) > 0 {
		s.mu.Unlock()
		s.notify(evs)
		s.mu.Lock() // 调用方 defer 解锁
	}
	return nil
}

// Commit 执行一次 checkpoint。条件写失败返回 ErrConflict，什么也不改。
// 与 sqlite 一样不检查墓碑（sqlite 按 id 查行，不看 status）。
func (s *Store) Commit(ctx context.Context, cp store.Checkpoint) error {
	if err := s.lock(); err != nil {
		return err
	}
	x := s.sessions[cp.SessionID]
	if x == nil {
		s.mu.Unlock()
		return store.ErrNotFound
	}
	// 先校验纪元再校验 log_len（与 sqlite 一致，02 2.2 fencing）。
	if cp.Epoch != 0 && x.leaseEpoch != cp.Epoch {
		s.mu.Unlock()
		return fmt.Errorf("%w: 期望纪元 %d，实际 %d", store.ErrLeaseLost, cp.Epoch, x.leaseEpoch)
	}
	if x.logLen != cp.ExpectLogLen {
		s.mu.Unlock()
		return fmt.Errorf("%w: 期望 log_len=%d，实际 %d", store.ErrConflict, cp.ExpectLogLen, x.logLen)
	}
	// 先把可能失败的序列化全部做完，再动数据。
	hash, shell, hb, err := splitHeader(cp.Header)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	bodies := make([][]byte, len(cp.Append))
	for i, e := range cp.Append {
		if bodies[i], err = json.Marshal(e); err != nil {
			s.mu.Unlock()
			return err
		}
	}
	for i := range cp.Append {
		idx := cp.AppendFrom + i
		_, exists := x.entries[idx]
		if exists && (cp.TruncateFrom == nil || idx < *cp.TruncateFrom) {
			s.mu.Unlock()
			return fmt.Errorf("memstore: 条目 idx=%d 已存在", idx) // sqlite 主键冲突
		}
	}

	now := s.now()
	seq := x.nextSeq
	var evs []bus.Envelope
	if cp.TruncateFrom != nil {
		from := *cp.TruncateFrom
		idxs := make([]int, 0)
		for idx := range x.entries {
			if idx >= from {
				idxs = append(idxs, idx)
			}
		}
		sort.Ints(idxs)
		for _, idx := range idxs {
			e := x.entries[idx]
			x.archive = append(x.archive, archived{idx: idx, body: e.body, seq: e.seq, truncatedAt: now, rewindSeq: seq})
			delete(x.entries, idx)
		}
		x.events = append(x.events, evRow{cursor: int64(seq), kind: store.EventRewind, to: from, at: now})
		evs = append(evs, bus.Envelope{V: bus.Version, Seq: int64(seq), At: now, Session: cp.SessionID, Kind: store.EventRewind,
			Data: map[string]any{"to": from}})
		seq++ // 每次截断占一个 sync_seq
	}
	for i, e := range cp.Append {
		idx := cp.AppendFrom + i
		x.entries[idx] = entry{body: bodies[i], at: e.At, seq: seq}
		x.events = append(x.events, evRow{cursor: int64(seq), kind: store.EventEntry, idx: idx, body: bodies[i], at: e.At})
		evs = append(evs, bus.Envelope{V: bus.Version, Seq: int64(seq), At: e.At, Session: cp.SessionID, Kind: store.EventEntry,
			Data: map[string]any{"idx": idx, "entry": json.RawMessage(bodies[i])}})
		seq++
	}
	// 其他持久事件排在条目之后，各占一个游标（06 10.2.1 规则 2）。
	for _, ev := range cp.Events {
		data := cloneRaw(ev.Data)
		if len(data) == 0 {
			data = json.RawMessage(`{}`)
		}
		x.events = append(x.events, evRow{cursor: int64(seq), kind: ev.Kind, data: data, at: now})
		evs = append(evs, bus.Envelope{V: bus.Version, Seq: int64(seq), At: now, Session: cp.SessionID, Kind: ev.Kind,
			Data: json.RawMessage(append([]byte(nil), data...))})
		seq++
	}
	s.putShell(hash, shell)
	if r := cp.Request; r != nil {
		if len(r.ShellBody) > 0 {
			s.putShell(r.ShellHash, r.ShellBody)
		}
		rr := *r
		rr.ShellBody = nil
		rr.CompactionView, rr.Composition = cloneRaw(r.CompactionView), cloneRaw(r.Composition)
		old, ok := x.requests[r.RequestID]
		if !ok {
			x.reqOrder = append(x.reqOrder, r.RequestID)
			old.createdAt = now
		}
		x.requests[r.RequestID] = request{r: rr, createdAt: old.createdAt} // 覆盖更新保留 created_at
	}
	for _, id := range cp.ConsumeInbox {
		q := s.inbox[cp.SessionID]
		for i := range q {
			if q[i].it.ItemID == id {
				q[i].consumed = true
			}
		}
	}
	for _, sp := range cp.Spans {
		s.putSpan(cp.SessionID, sp)
	}
	for _, o := range cp.Outbox {
		dup := false
		for i := range s.outbox {
			if s.outbox[i].ID == o.ID {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		it := o
		it.SessionID = cp.SessionID
		if it.CreatedAt == 0 {
			it.CreatedAt = now
		}
		s.outbox = append(s.outbox, outboxRow{OutboxItem: it})
	}
	run := cp.RunState
	if run == "" {
		run = "idle"
	}
	x.header, x.shellHash, x.logLen, x.nextSeq, x.runState = hb, hash, cp.Header.LogLen, seq, run
	if cp.Preview != "" {
		x.preview = cp.Preview
	}
	x.updatedAt = now
	s.mu.Unlock()
	s.notify(evs)
	return nil
}

// putSpan span_id 全局唯一（与 sqlite 主键一致）：已存在只更新 End / Status / Attrs。
func (s *Store) putSpan(sid string, sp store.Span) {
	sp.Attrs = cloneRaw(sp.Attrs)
	for _, x := range s.sessions {
		for i := range x.spans {
			if x.spans[i].s.ID == sp.ID {
				x.spans[i].s.End, x.spans[i].s.Status, x.spans[i].s.Attrs = sp.End, sp.Status, sp.Attrs
				return
			}
		}
	}
	x := s.sessions[sid]
	x.spans = append(x.spans, spanRow{s: sp, sid: sid})
}

// Load 读头部 + 外壳，并从 base（默认 compaction_base）起读条目。
func (s *Store) Load(ctx context.Context, id string, base *int) (*store.Loaded, error) {
	if err := s.lock(); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	x := s.live(id)
	if x == nil {
		return nil, store.ErrNotFound
	}
	hdr, err := session.UnmarshalHeader(x.header)
	if err != nil {
		return nil, err
	}
	body, ok := s.shells[x.shellHash]
	if !ok {
		return nil, fmt.Errorf("外壳 %s: 不存在", x.shellHash)
	}
	if string(body) != "null" {
		var cfg session.Config
		if err := json.Unmarshal(body, &cfg); err != nil {
			return nil, err
		}
		hdr.Config = &cfg
	}
	from := hdr.CompactionBase
	if base != nil {
		from = *base
	}
	entries, err := x.read(from, -1)
	if err != nil {
		return nil, err
	}
	m := x.meta
	m.Meta, m.UseConfig = objOr(m.Meta), objOr(m.UseConfig)
	return &store.Loaded{Meta: m, RunState: x.runState, Snapshot: session.Snapshot{Header: hdr, Base: from, Entries: entries},
		Cursor: int64(x.nextSeq - 1)}, nil
}

func objOr(b json.RawMessage) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("{}")
	}
	return cloneRaw(b)
}

func (x *sess) read(from, to int) ([]*session.Entry, error) {
	idxs := make([]int, 0, len(x.entries))
	for idx := range x.entries {
		if idx >= from && (to < 0 || idx < to) {
			idxs = append(idxs, idx)
		}
	}
	sort.Ints(idxs)
	var out []*session.Entry
	for _, idx := range idxs {
		var e session.Entry
		if err := json.Unmarshal(x.entries[idx].body, &e); err != nil {
			return nil, err
		}
		out = append(out, &e)
	}
	return out, nil
}

// Entries 按完整日志下标读 [from, to)；to < 0 表示到末尾。未知会话返回空（与 sqlite 一致）。
func (s *Store) Entries(ctx context.Context, id string, from, to int) ([]*session.Entry, error) {
	if err := s.lock(); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	x := s.sessions[id]
	if x == nil {
		return nil, nil
	}
	return x.read(from, to)
}

// Exists 报告会话是否存在（未删除）。
func (s *Store) Exists(ctx context.Context, id string) (bool, error) {
	if err := s.lock(); err != nil {
		return false, err
	}
	defer s.mu.Unlock()
	return s.live(id) != nil, nil
}

// GetRequest 读请求记录；不存在返回 (nil, nil)。
func (s *Store) GetRequest(ctx context.Context, sessionID, requestID string) (*store.RequestRecord, error) {
	if err := s.lock(); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	x := s.sessions[sessionID]
	if x == nil {
		return nil, nil
	}
	r, ok := x.requests[requestID]
	if !ok {
		return nil, nil
	}
	out := r.r
	out.CompactionView, out.Composition = cloneRaw(out.CompactionView), cloneRaw(out.Composition)
	return &out, nil
}

// ShellBody 按哈希读外壳原文。
func (s *Store) ShellBody(ctx context.Context, hash string) ([]byte, error) {
	if err := s.lock(); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	b, ok := s.shells[hash]
	if !ok {
		return nil, store.ErrNotFound
	}
	return append([]byte(nil), b...), nil
}

// Shell 按哈希读外壳（Config）。
func (s *Store) Shell(ctx context.Context, hash string) (*session.Config, error) {
	b, err := s.ShellBody(ctx, hash)
	if err != nil {
		return nil, err
	}
	var c session.Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// DeleteSession 置墓碑 → 依次调用 cleaners（不持锁）→ 清理全部数据。
func (s *Store) DeleteSession(ctx context.Context, id string, cleaners []store.Cleaner) error {
	if err := s.lock(); err != nil {
		return err
	}
	x := s.sessions[id]
	if x == nil {
		s.mu.Unlock()
		return store.ErrNotFound
	}
	x.status = "deleted"
	s.mu.Unlock()
	for i, c := range cleaners {
		if err := c(ctx, id); err != nil {
			return fmt.Errorf("清理回调 %d: %w（墓碑保留，可重试）", i, err)
		}
	}
	if err := s.lock(); err != nil {
		return err
	}
	delete(s.sessions, id)
	delete(s.inbox, id)
	delete(s.tools, id)
	for corr, p := range s.pending {
		if p.SessionID == id {
			delete(s.pending, corr)
		}
	}
	kept := s.outbox[:0]
	for _, o := range s.outbox { // outboxRow
		if o.SessionID != id {
			kept = append(kept, o)
		}
	}
	s.outbox = kept
	tk := s.traces[:0]
	for _, ev := range s.traces {
		if ev.Session != id {
			tk = append(tk, ev)
		}
	}
	s.traces = tk
	s.mu.Unlock()
	return nil
}

// ListSessions 按更新时间倒序列出未删除会话；before > 0 时只列 updated_at < before 的。
func (s *Store) ListSessions(ctx context.Context, before int64, limit int, topOnly bool) ([]store.SessionInfo, error) {
	return s.ListSessionsIn(ctx, "", before, limit, topOnly)
}

// ListSessionsIn 同 ListSessions，只列属于 workspace 的会话（空 = 不过滤）。
func (s *Store) ListSessionsIn(ctx context.Context, workspace string, before int64, limit int, topOnly bool) ([]store.SessionInfo, error) {
	if err := s.lock(); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	var out []store.SessionInfo
	for _, x := range s.sessions {
		if x.status == "deleted" || (before > 0 && x.updatedAt >= before) || (topOnly && x.meta.ParentID != "") ||
			(workspace != "" && x.meta.Workspace != workspace) {
			continue
		}
		out = append(out, x.info())
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt != out[j].UpdatedAt {
			return out[i].UpdatedAt > out[j].UpdatedAt
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (x *sess) info() store.SessionInfo {
	i := store.SessionInfo{ID: x.meta.ID, Title: x.meta.Title, Preview: x.preview, RunState: x.runState,
		Origin: x.meta.Origin, ParentID: x.meta.ParentID, Workspace: x.meta.Workspace, UpdatedAt: x.updatedAt}
	if m := x.meta.Meta; len(m) > 0 && string(m) != "{}" {
		i.Meta = cloneRaw(m)
	}
	return i
}

// UpdateSessionMeta 改标题与业务键，同时写 meta 事件（与 sqlite 同语义）。
func (s *Store) UpdateSessionMeta(ctx context.Context, id string, title *string, set map[string]any) error {
	if err := s.lock(); err != nil {
		return err
	}
	x := s.live(id)
	if x == nil {
		s.mu.Unlock()
		return store.ErrNotFound
	}
	meta, err := store.MergeMeta(x.meta.Meta, set)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if title != nil {
		x.meta.Title = *title
	}
	x.meta.Meta = meta
	data, _ := json.Marshal(map[string]any{"title": x.meta.Title, "meta": meta})
	now := s.now()
	seq := int64(x.nextSeq)
	x.events = append(x.events, evRow{cursor: seq, kind: store.EventMeta, data: data, at: now})
	x.nextSeq++
	s.mu.Unlock()
	s.notify([]bus.Envelope{{V: bus.Version, Seq: seq, At: now, Session: id, Kind: store.EventMeta,
		Data: json.RawMessage(append([]byte(nil), data...))}})
	return nil
}

// SessionsWithMeta 业务键 key 为 true 的未删除会话（按更新时间倒序）。
func (s *Store) SessionsWithMeta(ctx context.Context, key string, limit int) ([]store.SessionInfo, error) {
	if err := s.lock(); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	var out []store.SessionInfo
	for _, x := range s.sessions {
		if x.status == "deleted" {
			continue
		}
		var m map[string]any
		_ = json.Unmarshal(x.meta.Meta, &m)
		if m[key] == true {
			out = append(out, x.info())
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt != out[j].UpdatedAt {
			return out[i].UpdatedAt > out[j].UpdatedAt
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

var _ store.SessionMetaStore = (*Store)(nil)

// SessionStats 汇总会话用量与最近一次请求组成。未知会话返回零值统计。
func (s *Store) SessionStats(ctx context.Context, sessionID string) (store.Stats, error) {
	var st store.Stats
	if err := s.lock(); err != nil {
		return st, err
	}
	defer s.mu.Unlock()
	x := s.sessions[sessionID]
	if x == nil {
		return st, nil
	}
	for _, sp := range x.spans {
		st.AddSpan(sp.s.Kind, sp.s.Status, string(sp.s.Attrs))
	}
	var last *request
	for _, id := range x.reqOrder {
		r := x.requests[id]
		if last == nil || r.createdAt >= last.createdAt {
			last = &r
		}
	}
	if last != nil {
		st.LastRequest = last.r.RequestID
		st.Composition = cloneRaw(last.r.Composition)
	}
	return st, nil
}

// —— 事件 ——//

// OnCommit 设置提交成功后的持久事件回调。
func (s *Store) OnCommit(f func(bus.Envelope)) {
	s.cbMu.Lock()
	s.onCommit = f
	s.cbMu.Unlock()
}

func (s *Store) notify(evs []bus.Envelope) {
	s.cbMu.Lock()
	f := s.onCommit
	s.cbMu.Unlock()
	if f == nil {
		return
	}
	for _, e := range evs {
		f(e)
	}
}

// Replay 实现 bus.Source：seq > after 的条目追加与回退事件（升序）及水位。会话不存在返回错误（墓碑仍可回放）。
func (s *Store) Replay(ctx context.Context, sessionID string, after int64) ([]bus.Envelope, int64, error) {
	if err := s.lock(); err != nil {
		return nil, 0, err
	}
	defer s.mu.Unlock()
	x := s.sessions[sessionID]
	if x == nil {
		return nil, 0, store.ErrNotFound
	}
	if after < x.eventFloor {
		return nil, 0, fmt.Errorf("%w: 游标 %d 早于 %d", store.ErrCursorExpired, after, x.eventFloor)
	}
	// 只读事件序列（06 10.2.1 规则 4），形状与 sqlite 一致：entry {idx, entry}、rewind {to}、其余原样。
	var out []bus.Envelope
	for _, ev := range x.events {
		if ev.cursor <= after {
			continue
		}
		env := bus.Envelope{V: bus.Version, Seq: ev.cursor, At: ev.at, Session: sessionID, Kind: ev.kind}
		switch ev.kind {
		case store.EventEntry:
			env.Data = map[string]any{"idx": ev.idx, "entry": json.RawMessage(append([]byte(nil), ev.body...))}
		case store.EventRewind:
			env.Data = map[string]any{"to": ev.to}
		default:
			env.Data = json.RawMessage(append([]byte(nil), ev.data...))
		}
		out = append(out, env)
	}
	return out, int64(x.nextSeq - 1), nil
}

// Export 把会话全部数据写成 JSONL（记录种类与 sqlite 相同：session / entry / archived / request / tool / span）。
func (s *Store) Export(ctx context.Context, sessionID string, w io.Writer) error {
	if err := s.lock(); err != nil {
		return err
	}
	defer s.mu.Unlock()
	x := s.sessions[sessionID]
	if x == nil {
		return store.ErrNotFound
	}
	enc := json.NewEncoder(w)
	emit := func(kind string, v any) error { return enc.Encode(map[string]any{"kind": kind, "data": v}) }
	cfg := s.shells[x.shellHash]
	if len(cfg) == 0 {
		cfg = []byte("null")
	}
	m := x.meta
	if err := emit("session", map[string]any{
		"id": m.ID, "agent_id": m.AgentID, "agent_hash": m.AgentHash, "creator": m.Creator,
		"parent_id": m.ParentID, "origin": m.Origin, "fork_upto": m.ForkUpto,
		"status": x.status, "run_state": x.runState, "title": m.Title, "log_len": x.logLen,
		"updated_at": x.updatedAt, "header": json.RawMessage(x.header), "config": json.RawMessage(cfg),
	}); err != nil {
		return err
	}
	idxs := make([]int, 0, len(x.entries))
	for idx := range x.entries {
		idxs = append(idxs, idx)
	}
	sort.Ints(idxs)
	for _, idx := range idxs {
		e := x.entries[idx]
		if err := emit("entry", map[string]any{"idx": idx, "sync_seq": e.seq, "entry": json.RawMessage(e.body)}); err != nil {
			return err
		}
	}
	arch := append([]archived(nil), x.archive...)
	sort.SliceStable(arch, func(i, j int) bool {
		if arch[i].rewindSeq != arch[j].rewindSeq {
			return arch[i].rewindSeq < arch[j].rewindSeq
		}
		return arch[i].idx < arch[j].idx
	})
	for _, a := range arch {
		if err := emit("archived", map[string]any{"idx": a.idx, "rewind_seq": a.rewindSeq, "truncated_at": a.truncatedAt,
			"entry": json.RawMessage(a.body)}); err != nil {
			return err
		}
	}
	for _, id := range x.reqOrder {
		r := x.requests[id]
		if err := emit("request", map[string]any{"request_id": r.r.RequestID, "shell_hash": r.r.ShellHash,
			"prefix_base": r.r.PrefixBase, "prefix_end": r.r.PrefixEnd, "compaction_view": rawOrNil(r.r.CompactionView),
			"final_hash": r.r.FinalHash, "composition": rawOrNil(r.r.Composition),
			"created_at": r.createdAt}); err != nil {
			return err
		}
	}
	for _, t := range s.sortedTools(sessionID) {
		if err := emit("tool", map[string]any{"call_id": t.r.CallID, "status": t.r.Status, "kind": t.r.Kind,
			"args_hash": t.r.ArgsHash, "decision": t.r.Decision, "output": rawOrNil(json.RawMessage(t.r.Output)),
			"created_at": t.created}); err != nil {
			return err
		}
	}
	spans := append([]spanRow(nil), x.spans...)
	sort.SliceStable(spans, func(i, j int) bool { return spans[i].s.Start.Before(spans[j].s.Start) })
	for _, sp := range spans {
		var end int64
		if !sp.s.End.IsZero() {
			end = sp.s.End.UnixMilli()
		}
		if err := emit("span", map[string]any{"span_id": sp.s.ID, "parent": sp.s.Parent, "kind": sp.s.Kind, "name": sp.s.Name,
			"started_at": sp.s.Start.UnixMilli(), "ended_at": end, "status": sp.s.Status, "attrs": rawOrNil(sp.s.Attrs)}); err != nil {
			return err
		}
	}
	return nil
}

func rawOrNil(b json.RawMessage) any {
	if len(b) == 0 {
		return nil
	}
	if !json.Valid(b) {
		return string(b)
	}
	return b
}

// —— 定义 ——//

// PutDefinition 写入新版本，返回版本号（从 1 起）；同时记整包内容哈希。
func (s *Store) PutDefinition(ctx context.Context, kind, id, body string) (int, string, error) {
	if err := s.lock(); err != nil {
		return 0, "", err
	}
	defer s.mu.Unlock()
	k := kind + "\x00" + id
	h := store.HashBytes([]byte(body))
	s.defs[k] = append(s.defs[k], def{body: body, hash: h, at: s.now()})
	return len(s.defs[k]), h, nil
}

// GetDefinition 读指定版本；version <= 0 取最新。不存在返回 ErrNotFound。
func (s *Store) GetDefinition(ctx context.Context, kind, id string, version int) (string, int, error) {
	if err := s.lock(); err != nil {
		return "", 0, err
	}
	defer s.mu.Unlock()
	vs := s.defs[kind+"\x00"+id]
	if version <= 0 {
		version = len(vs)
	}
	if version <= 0 || version > len(vs) {
		return "", 0, store.ErrNotFound
	}
	return vs[version-1].body, version, nil
}

// ListDefinitions 列出 kind 下出现过的定义 id（每个 id 一次，按 id 升序）。
func (s *Store) ListDefinitions(ctx context.Context, kind string) ([]string, error) {
	if err := s.lock(); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	prefix := kind + "\x00"
	var out []string
	for k := range s.defs {
		if strings.HasPrefix(k, prefix) {
			out = append(out, strings.TrimPrefix(k, prefix))
		}
	}
	sort.Strings(out)
	return out, nil
}

// DefinitionHash 读指定版本的内容哈希（测试用；sqlite 经 SQL 查 definitions.hash）。
func (s *Store) DefinitionHash(kind, id string, version int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	vs := s.defs[kind+"\x00"+id]
	if version <= 0 {
		version = len(vs)
	}
	if version <= 0 || version > len(vs) {
		return ""
	}
	return vs[version-1].hash
}

// FindSessionByKey 按会话键查会话 id（13 13.3 B19）。
func (s *Store) FindSessionByKey(_ context.Context, agentID, creator, key string) (string, error) {
	if err := s.lock(); err != nil {
		return "", err
	}
	defer s.mu.Unlock()
	if key == "" { // 与 sqlite 一致：没有键的会话不参与查找
		return "", store.ErrNotFound
	}
	for _, x := range s.sessions {
		if x.meta.AgentID == agentID && x.meta.Creator == creator && x.meta.SessionKey == key {
			return x.meta.ID, nil
		}
	}
	return "", store.ErrNotFound
}
