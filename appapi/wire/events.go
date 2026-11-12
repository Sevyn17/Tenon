package wire

// 事件（10.5）：命名统一为「领域.动作」，动作用过去式。持久事件带 cursor、写进事件表；易失事件不带。

// Envelope 事件信封。Data 是下面某个载荷类型。
type Envelope struct {
	V       int    `json:"v"`
	Session string `json:"session"`
	Kind    string `json:"kind"`
	At      int64  `json:"at"`
	Cursor  int64  `json:"cursor,omitempty"`
	Turn    uint32 `json:"turn,omitempty"`
	Data    any    `json:"data"`
}

// 持久事件（表一）。
const (
	EvEntryAppended   = "entry.appended"
	EvLogRewound      = "log.rewound"
	EvLogCompacted    = "log.compacted"
	EvStateChanged    = "session.state_changed"
	EvModeChanged     = "session.mode_changed"
	EvSettingsChanged = "session.settings_changed"
	EvInteractionOpen = "interaction.opened"
	EvInteractionDone = "interaction.resolved"
	EvSessionClosed   = "session.closed"
	EvSessionForked   = "session.forked"
	EvSessionUpdated  = "session.updated"
)

// 易失事件（表二）。
const (
	EvTurnStarted      = "turn.started"
	EvTurnEnded        = "turn.ended"
	EvTurnRejected     = "turn.rejected"
	EvMessageStarted   = "message.started"
	EvMessageDelta     = "message.delta"
	EvMessageEnded     = "message.ended"
	EvMessageDiscarded = "message.discarded"
	EvToolStarted      = "tool.started"
	EvToolArgsDelta    = "tool.args_delta"
	EvToolRunning      = "tool.running"
	EvToolProgress     = "tool.progress"
	EvToolEnded        = "tool.ended"
	EvNotice           = "notice"
	EvSynced           = "session.synced"
	EvLagged           = "session.lagged"
	EvSessionsChanged  = "sessions.changed"
)

// Durable 事件是否持久（带 cursor、写事件表）。
func Durable(kind string) bool {
	switch kind {
	case EvEntryAppended, EvLogRewound, EvLogCompacted, EvStateChanged, EvModeChanged, EvSettingsChanged,
		EvInteractionOpen, EvInteractionDone, EvSessionClosed, EvSessionForked, EvSessionUpdated:
		return true
	}
	return false
}

// Public 事件是否公开推给客户端（不在两张表里的一律不推，10.5 事件过滤）。
func Public(kind string) bool {
	if Durable(kind) {
		return true
	}
	switch kind {
	case EvTurnStarted, EvTurnEnded, EvTurnRejected, EvMessageStarted, EvMessageDelta, EvMessageEnded,
		EvMessageDiscarded, EvToolStarted, EvToolArgsDelta, EvToolRunning, EvToolProgress, EvToolEnded,
		EvNotice, EvSynced, EvLagged, EvSessionsChanged:
		return true
	}
	return false
}

// —— 持久事件载荷 ——

// EntryAppended 会话日志追加了一条条目。
type EntryAppended struct {
	Entry Entry `json:"entry"`
}

// LogRewound 日志被回退（rewind）到某个位置。
type LogRewound struct {
	To                 int      `json:"to"`
	VoidedInteractions []string `json:"voided_interactions"`
}

// LogCompacted 日志被压缩：upto 之前的条目由摘要替代。
type LogCompacted struct {
	Upto     int `json:"upto"`
	EntryIdx int `json:"entry_idx"`
}

// StateChanged 会话运行状态变化（idle / waiting_model / waiting_tools）。
type StateChanged struct {
	RunState string `json:"run_state"`
}

// ModeChanged 会话权限档位切换。
type ModeChanged = Mode

// SettingsChanged 会话设置（标题、置顶等）变化。
type SettingsChanged struct {
	Settings Settings `json:"settings"`
}

// InteractionResolved 一个待应答的交互被解决了。
type InteractionResolved struct {
	ID      string `json:"id"`
	Session string `json:"session"`
	Outcome string `json:"outcome"`
	By      string `json:"by,omitempty"`
}

// SessionClosed 会话被关闭或删除。
type SessionClosed struct {
	Reason string `json:"reason"`
}

// SessionForked 会话被分叉成新的子会话。
type SessionForked struct {
	From string `json:"from"`
	Upto int    `json:"upto"`
}

// SessionUpdated session.update 落库（标题、置顶）。
type SessionUpdated struct {
	Title  string `json:"title"`
	Pinned bool   `json:"pinned"`
}

// —— 易失事件载荷 ——

// TurnStarted 一个回合开始。
type TurnStarted struct {
	Turn   uint32   `json:"turn"`
	Inputs []string `json:"inputs"`
}

// TurnEnded Kind: done / cancelled / max_steps / denied / failed（G7：不再是拼好的字符串）。
type TurnEnded struct {
	Turn    uint32 `json:"turn"`
	Kind    string `json:"kind"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// TurnRejected 一个回合在开始前被拒（守卫或预算）。
type TurnRejected struct {
	Inputs []string `json:"inputs"`
	Reason string   `json:"reason"`
}

// MessageStarted 一条助手消息开始（后续是 MessageDelta）。
type MessageStarted struct {
	StreamID string `json:"stream_id"`
	EntryID  string `json:"entry_id"`
	Part     string `json:"part"`
	Attempt  int    `json:"attempt"`
}

// MessageDelta 助手消息的正文增量。
type MessageDelta struct {
	StreamID string `json:"stream_id"`
	Text     string `json:"text"`
}

// StreamRef 流式片段的引用（stream_id + 序号）。
type StreamRef struct {
	StreamID string `json:"stream_id"`
}

// ToolStarted 一次工具调用开始（参数还在流式到达）。
type ToolStarted struct {
	CallID  string `json:"call_id"`
	Name    string `json:"name"`
	EntryID string `json:"entry_id"`
}

// ToolArgsDelta 工具参数的流式增量。
type ToolArgsDelta struct {
	CallID string `json:"call_id"`
	Text   string `json:"text"`
}

// ToolRunning 工具参数已齐、开始执行。
type ToolRunning struct {
	CallID string `json:"call_id"`
	Args   any    `json:"args"`
}

// ToolProgress 工具执行进度（易失，可丢）。
type ToolProgress struct {
	CallID string `json:"call_id"`
	Text   string `json:"text,omitempty"`
	Data   any    `json:"data,omitempty"`
}

// ToolEnded 一次工具调用结束（带结果或错误）。
type ToolEnded struct {
	CallID    string `json:"call_id"`
	Status    string `json:"status"`
	Preview   string `json:"preview"`
	OutputRef string `json:"output_ref,omitempty"`
}

// Notice 一条提示（易失）。
type Notice struct {
	Level  string `json:"level"` // info / warn
	Source string `json:"source,omitempty"`
	Text   string `json:"text"`
}

// CursorRef 一个游标位置。
type CursorRef struct {
	Cursor int64 `json:"cursor"`
}

// SessionsChanged 列表里一行的变化。deleted 为真时这一行已删除，其余字段无意义。
type SessionsChanged struct {
	ID        string `json:"id"`
	RunState  string `json:"run_state"`
	Title     string `json:"title"`
	UpdatedAt int64  `json:"updated_at"`
	Pinned    bool   `json:"pinned"`
	Deleted   bool   `json:"deleted,omitempty"`
}
