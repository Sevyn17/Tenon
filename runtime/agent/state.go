package agent

// Agent 的内部状态分组（2026-11-04 审查 P1-59）：每把锁连同它守的字段收进一个小类型，
// 方法只碰自己的字段；actor 独占的字段（提交缓冲、恢复状态）不加锁，单独成组。

import (
	"encoding/json"
	"sort"
	"sync"

	"ybase/runtime/reqlog"
	"ybase/runtime/session"
	"ybase/runtime/store"
)

// capsState 本轮的授权工具范围（信封 caps 交集，T3）。host 线程写，工具 goroutine 读。
type capsState struct {
	mu      sync.RWMutex
	list    []string // nil = 不限，空集 = 一个都不许
	guarded bool     // caps 守卫层已装
}

// markGuarded 标记守卫已装；已装过返回 false。
func (c *capsState) markGuarded() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.guarded {
		return false
	}
	c.guarded = true
	return true
}

func (c *capsState) set(caps []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if caps == nil {
		c.list = nil
	} else {
		c.list = append([]string{}, caps...)
	}
}

func (c *capsState) get() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.list == nil {
		return nil
	}
	return append([]string{}, c.list...)
}

func (c *capsState) allows(tool string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.list == nil {
		return true
	}
	for _, x := range c.list {
		if x == tool {
			return true
		}
	}
	return false
}

// traceState 本回合的 trace id、触发链与待落库的 trace 事件。工具 goroutine 也会记事件。
type traceState struct {
	mu     sync.Mutex
	id     string
	origin TurnOrigin
	events []store.TraceEvent
}

func (t *traceState) setOrigin(o TurnOrigin) {
	t.mu.Lock()
	t.origin = o
	t.mu.Unlock()
}

func (t *traceState) getOrigin() TurnOrigin {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.origin
}

func (t *traceState) setID(id string) {
	t.mu.Lock()
	t.id = id
	t.mu.Unlock()
}

func (t *traceState) getID() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.id
}

// add 记一条事件；没有 trace id 时不记。
func (t *traceState) add(session, kind, detail string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.id == "" {
		return
	}
	t.events = append(t.events, store.TraceEvent{Trace: t.id, Session: session, Kind: kind, Detail: detail})
}

func (t *traceState) take() []store.TraceEvent {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := t.events
	t.events = nil
	return out
}

// snapState 最近一次提交的快照（Committed 用；actor 之外的读者不碰 Core）。
type snapState struct {
	mu   sync.RWMutex
	snap *session.Snapshot
}

func (s *snapState) set(v session.Snapshot) {
	s.mu.Lock()
	s.snap = &v
	s.mu.Unlock()
}

// get 没有快照时 ok=false。
func (s *snapState) get() (session.Snapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.snap == nil {
		return session.Snapshot{}, false
	}
	return *s.snap, true
}

// toolSpans 工具调用 span（toolhub 从工具 goroutine 回调）与当前 step span id（工具 span 的父）。
type toolSpans struct {
	mu     sync.Mutex
	spans  []store.Span
	stepID string
}

func (t *toolSpans) setStep(id string) {
	t.mu.Lock()
	t.stepID = id
	t.mu.Unlock()
}

func (t *toolSpans) step() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stepID
}

// add 追加一条工具 span；parent 为空时挂在当前 step 下。
func (t *toolSpans) add(sp store.Span) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if sp.Parent == "" {
		sp.Parent = t.stepID
	}
	t.spans = append(t.spans, sp)
}

// snapshot 当前缓冲的拷贝与长度（提交成功后用 drop(n) 去掉已提交的部分）。
func (t *toolSpans) snapshot() ([]store.Span, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]store.Span(nil), t.spans...), len(t.spans)
}

func (t *toolSpans) drop(n int) {
	t.mu.Lock()
	t.spans = t.spans[n:]
	t.mu.Unlock()
}

// commitBuf 随下一次 checkpoint 一起提交的内容（actor 独占，不加锁）。
type commitBuf struct {
	committed int                // 存储里已提交的 log_len（条件写期望值）
	spans     []store.Span       // 待提交的 step / attempt / compact span（G2）
	outbox    []store.OutboxItem // 外发信封（08 9.6：外发与检查点同事务）
	consume   []string           // 删除的 inbox 项（session-host H7：core.Input 成功后同事务删除）

	// 已提交的会话状态（protocol 2，06 10.2.2 规则 2）：检查点按它与新头部比较，变了就随检查点写持久事件。
	known    bool
	run      string
	maxSteps uint32
	ext      map[string]string // 键 → 值的 JSON
}

// reset 提交成功后清空缓冲。
func (b *commitBuf) reset(logLen int) {
	b.committed = logLen
	b.spans, b.outbox, b.consume = nil, nil, nil
}

// remember 记下已提交的运行状态、设置与 Ext（打开会话与每次提交后调用）。
func (b *commitBuf) remember(run string, h session.Header) {
	b.known, b.run, b.maxSteps, b.ext = true, run, maxStepsOf(h), extJSON(h.Ext)
}

// stateEvents 新头部相对已提交状态的变化 → 持久事件（运行状态、设置、Ext 各项）。
func (b *commitBuf) stateEvents(run string, h session.Header) []store.Event {
	if !b.known {
		return nil
	}
	var out []store.Event
	if run != b.run {
		d, _ := json.Marshal(map[string]any{"run_state": run})
		out = append(out, store.Event{Kind: store.EventRunState, Data: d})
	}
	if ms := maxStepsOf(h); ms != b.maxSteps {
		d, _ := json.Marshal(map[string]any{"max_steps": ms})
		out = append(out, store.Event{Kind: store.EventSettings, Data: d})
	}
	now := extJSON(h.Ext)
	keys := make([]string, 0, len(now)+len(b.ext))
	for k := range now {
		keys = append(keys, k)
	}
	for k := range b.ext {
		if _, ok := now[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		v, ok := now[k]
		if ok && v == b.ext[k] {
			continue
		}
		if !ok {
			v = "null"
		}
		d, _ := json.Marshal(map[string]any{"key": k, "value": json.RawMessage(v)})
		out = append(out, store.Event{Kind: store.EventExt, Data: d})
	}
	return out
}

func maxStepsOf(h session.Header) uint32 {
	if h.Config == nil {
		return 0
	}
	return h.Config.Policy.MaxSteps
}

func extJSON(ext map[string]any) map[string]string {
	out := make(map[string]string, len(ext))
	for k, v := range ext {
		b, err := json.Marshal(v)
		if err != nil {
			continue
		}
		out[k] = string(b)
	}
	return out
}

// recovery Open 恢复出的状态（actor 独占）：待满足需求与原样重发的请求。
type recovery struct {
	need *session.Need    // 恢复出的待满足需求
	req  *session.Request // 恢复 WaitingModel 时按请求记录重组出的请求（原样重发，S7）
	view *reqlog.CompactionView
}
