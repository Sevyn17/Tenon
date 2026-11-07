package tenon

// SDK 的错误（07 7.3）：内核错误在出口处包成 SDK 的哨兵，errors.Is 用 SDK 的哨兵就能判，
// 原来的内核错误也仍然可判（双 %w）。2026-11-04 审查 P1-52：原先 History 查不到会话返回
// store.ErrNotFound，守卫拒绝返回 host.ErrNotAllowed，应用要 import 内核包才能判断。

import (
	"errors"
	"fmt"

	"tenon/runtime/agentdef"
	"tenon/runtime/host"
	"tenon/runtime/mail"
	"tenon/runtime/store"
)

// 错误（07 7.3；errors.Is 可判）。
var (
	ErrNotStarted = errors.New("tenon: 未 Start")
	ErrInvalid    = errors.New("tenon: 参数不合法")
	ErrNotFound   = errors.New("tenon: 未找到")
	// ErrDenied 守卫或联系名单拒绝（错误文本带原因）。
	ErrDenied = errors.New("tenon: 被拒绝")
	// ErrConflict 会话被另一写者推进或接管（条件写冲突、写租约失效）。
	ErrConflict = errors.New("tenon: 冲突")
)

// wrapErr 把内核错误包成对应的 SDK 哨兵；认不出的原样返回。
func wrapErr(err error) error {
	if err == nil {
		return nil
	}
	var sentinel error
	switch {
	case errors.Is(err, ErrNotStarted), errors.Is(err, ErrInvalid), errors.Is(err, ErrNotFound),
		errors.Is(err, ErrDenied), errors.Is(err, ErrConflict):
		return err // 已经是 SDK 错误
	case errors.Is(err, store.ErrNotFound), errors.Is(err, agentdef.ErrNotFound), errors.Is(err, host.ErrSessionNotFound):
		sentinel = ErrNotFound
	case errors.Is(err, host.ErrNotAllowed):
		sentinel = ErrDenied
	case errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrLeaseLost):
		sentinel = ErrConflict
	case errors.Is(err, mail.ErrInvalid):
		sentinel = ErrInvalid
	default:
		return err
	}
	return fmt.Errorf("%w: %w", sentinel, err)
}
