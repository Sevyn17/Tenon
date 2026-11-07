package appapi

import (
	"fmt"

	"tenon/appapi/wire"
)

// 错误码：与线上的 wire.Code* 同值（06 10.7），这里给应用层（apps、适配层）一个短名字。
const (
	CodeBadRequest   = wire.CodeBadRequest
	CodeNotFound     = wire.CodeNotFound
	CodeBusy         = wire.CodeBusy
	CodeNotIdle      = wire.CodeNotIdle
	CodeConflict     = wire.CodeConflict
	CodeUnauthorized = wire.CodeUnauthorized
	CodeUnsupported  = wire.CodeUnsupported
	CodeInternal     = wire.CodeInternal
	CodeNeedConfirm  = wire.CodeNeedConfirm
)

// Error 带稳定错误码的错误；JSON 形态 {code, message, retry, details?}，与 wire.Error 同形。
// Message 与原错误文本一致（CLI 原样打印）；Details 是结构化补充（如 conflict 的 reason）。
type Error struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Retry   bool           `json:"retry"`
	Details map[string]any `json:"details,omitempty"`
	err     error
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.err }

// Errorf 构造指定错误码的错误。
func Errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// wrap 方法统一出口：非 nil 错误都按 AsError 归类。
func wrap(err error) error {
	if err == nil {
		return nil
	}
	return AsError(err)
}
