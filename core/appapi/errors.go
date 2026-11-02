package appapi

import (
	"errors"
	"fmt"

	"ybase-kernel/core/host"
	"ybase-kernel/core/session"
	"ybase-kernel/core/store"
)

// 错误码（docs/90 - 归档/10 - 框架模块/12 - 应用接口层.md §五）。同版本内只增不改。
const (
	CodeBadRequest   = "bad_request"  // 参数不合法
	CodeNotFound     = "not_found"    // 会话 / 档位 / 工件不存在
	CodeBusy         = "busy"         // 命令队列满（host.ErrBusy），可重试
	CodeNotIdle      = "not_idle"     // 需空闲时使用的命令在回合中调用
	CodeConflict     = "conflict"     // 会话被另一进程写入或接管（store.ErrConflict / ErrLeaseLost）或目标已存在
	CodeUnauthorized = "unauthorized" // api-server 令牌 / Origin / Host 检查失败
	CodeUnsupported  = "unsupported"  // 协议版本、命令或能力不支持
	CodeInternal     = "internal"     // 其他
	CodeNeedConfirm  = "need_confirm" // 项目档位放宽了权限且未确认过：带 confirm=true 重发
)

// Error 带稳定错误码的错误；JSON 形态 {code, message, retry}。Message 与原错误文本一致（CLI 原样打印）。
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Retry   bool   `json:"retry"`
	err     error
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.err }

// Errorf 构造指定错误码的错误。
func Errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// AsError 把任意错误归类为 *Error（nil → nil）。已是 *Error 的原样返回。
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	code, retry := CodeInternal, false
	switch {
	case errors.Is(err, host.ErrBusy):
		code, retry = CodeBusy, true
	case errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrLeaseLost):
		code = CodeConflict
	case errors.Is(err, store.ErrNotFound), errors.Is(err, host.ErrClosed):
		code = CodeNotFound
	case errors.Is(err, session.ErrNotIdle), errors.Is(err, session.ErrNotWaiting), errors.Is(err, session.ErrState):
		code = CodeNotIdle
	}
	return &Error{Code: code, Message: err.Error(), Retry: retry, err: err}
}

// wrap 方法统一出口：非 nil 错误都转成 *Error。
func wrap(err error) error {
	if err == nil {
		return nil
	}
	return AsError(err)
}
