package session

// 错误模型（S5 §2 原语；M4 §5.3 S1 / S2 / S3）。
// 全部哨兵可用 errors.Is 判定；载荷错误（StateError / FactError）实现 Unwrap。

import (
	"errors"
	"fmt"
)

// ErrState：状态机错误总哨兵（R7：核心不 panic，全部状态机错误 errors.Is 命中它）。
var ErrState = errors.New("session: state error")

// 具体状态错误（映射 M4 §5.4：SESS_STATE 409 等）。
var (
	// ErrNotIdle：等待态调用 Idle 专属命令（input / resume / rewind / record）。
	ErrNotIdle = fmt.Errorf("%w: 当前状态不支持该操作（需 Idle）", ErrState)
	// ErrNotWaiting：Idle 调用等待态专属命令（steer / cancel / abort / submit）。
	ErrNotWaiting = fmt.Errorf("%w: 当前状态不支持该操作（需等待态）", ErrState)
	// ErrToolsOpen：工具调用的结果还没到齐时插入条目——会拆开调用与结果（R14），厂商会拒绝该请求。
	ErrToolsOpen = fmt.Errorf("%w: 工具结果未到齐，不能插入条目", ErrState)
	// ErrReadOnly：存在不支持的条目类型，会话只读打开（R15）。
	ErrReadOnly = fmt.Errorf("%w: 会话只读（存在不支持的条目类型）", ErrState)
	// ErrExtTooLarge：ext / 单条 meta 超过大小上限——拒不静默截断（S4 / R10）。
	ErrExtTooLarge = fmt.Errorf("%w: 扩展数据超过大小上限", ErrState)
	// ErrBadRewind：rewind 目标不是回合起点（T4）。
	ErrBadRewind = fmt.Errorf("%w: rewind 目标必须是回合起点", ErrState)
	// ErrBadResume：resume 末尾不是 User / ToolResult（M4 §7 resume）。
	ErrBadResume = fmt.Errorf("%w: resume 末尾不是 User 或 ToolResult 条目", ErrState)
	// ErrNoTurn：无进行中的回合可恢复。
	ErrNoTurn = fmt.Errorf("%w: 没有可恢复的回合", ErrState)
	// ErrBadArgs：模型请求的工具参数 JSON 非法（S3）。
	ErrBadArgs = fmt.Errorf("%w: 工具参数 JSON 非法", ErrState)
)

// FactError：事实错误（S2：过期的结果一律拒收——宿主只记日志，不回客户端）。
type FactError struct {
	Kind string // stale / unknown_call
	Msg  string
}

func (e *FactError) Error() string { return "session: 事实错误 " + e.Kind + ": " + e.Msg }
func (e *FactError) Unwrap() error { return ErrState }

// ErrStale / ErrUnknownCall 构造器（S2 / S6）。
func errStale(msg string) error       { return &FactError{Kind: "stale", Msg: msg} }
func errUnknownCall(msg string) error { return &FactError{Kind: "unknown_call", Msg: msg} }

// Denied：会话协议拒绝载荷（与错误码表共用，M4 §5.4）。
type Denied struct {
	Code       string
	Message    string
	Action     string
	RetryAfter int64
}

func (d Denied) Error() string { return d.Code + ": " + d.Message }
