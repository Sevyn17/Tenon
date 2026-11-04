package sqlite

// 共享类型与错误已上移到 runtime/store（T1a）；此处保留别名，调用方无需改动。

import "ybase/runtime/store"

// 编译期断言：*DB 实现 store.Store。
var _ store.Store = (*DB)(nil)

// Tenant：单机恒为 local（data-contract D4）。
const Tenant = store.Tenant

// 错误（与 store 包同一实例，errors.Is 互通）。
var (
	ErrConflict      = store.ErrConflict
	ErrNotFound      = store.ErrNotFound
	ErrStateMismatch = store.ErrStateMismatch
	ErrLeaseLost     = store.ErrLeaseLost
)

// 共享类型别名。
type (
	SessionMeta    = store.SessionMeta
	RequestRecord  = store.RequestRecord
	Span           = store.Span
	Checkpoint     = store.Checkpoint
	Loaded         = store.Loaded
	Cleaner        = store.Cleaner
	InboxItem      = store.InboxItem
	ToolRecord     = store.ToolRecord
	SessionInfo    = store.SessionInfo
	Stats          = store.Stats
)

// 常量。
const (
	InboxInput    = store.InboxInput
	InboxNote     = store.InboxNote
	ToolStarted   = store.ToolStarted
	ToolPending   = store.ToolPending
	ToolDelivered = store.ToolDelivered
	ToolDone      = store.ToolDone
	EventEntry    = store.EventEntry
	EventRewind   = store.EventRewind
)

// ShellHash 计算 Config 的外壳哈希（与存储一致）。
var ShellHash = store.ShellHash
