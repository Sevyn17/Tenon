package agent

// 回合结束原因（客户端改进 M2 / C-1、N-5）：结束原因进持久状态（store.LastEnd），
// 失败时错误码与人话分开（EndReason.Code 只放稳定码，Message 放说明）。

import (
	"errors"
	"unicode/utf8"

	"github.com/Sevyn17/tenon/runtime/modelhub"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
)

// CodeTurnFailed 兜底失败码：错误不是 modelhub.Failure（没有稳定码）时用它，原始错误放 Message。
const CodeTurnFailed = "TURN_FAILED"

// ErrorReason 把错误转成 Failed 结束原因：modelhub.Failure 取它的稳定码与说明；
// 其他错误用 fallback 作码（空 = CodeTurnFailed），err.Error() 作说明。说明截到 store.LastEndMessageMax 字节。
func ErrorReason(err error, fallback string) session.EndReason {
	if fallback == "" {
		fallback = CodeTurnFailed
	}
	var f *modelhub.Failure
	if errors.As(err, &f) && f.Code != "" {
		return session.EndReason{Kind: session.EndFailed, Code: string(f.Code), Message: trimMessage(f.Message)}
	}
	r := session.EndReason{Kind: session.EndFailed, Code: fallback}
	if err != nil {
		r.Message = trimMessage(err.Error())
	}
	return r
}

// SetLastEnd 记下本回合的结束原因，随下一次检查点（与 run_state 同一事务）写进会话行，
// 并让那次检查点一定写一条带 last_end 的 run_state 持久事件。turn 是回合号（LastTurn），
// Message 截到 store.LastEndMessageMax 字节。多次调用以最后一次为准。
func (a *Agent) SetLastEnd(turn uint32, end session.EndReason) {
	end.Message = trimMessage(end.Message)
	a.buf.lastEnd = &store.LastEnd{Turn: turn, End: end, At: a.clock().UnixMilli()}
}

// LastTurn 最后一条条目所属的回合号（回合结束时就是刚结束的回合）；没有条目时为 0。
// 与宿主发 turn_end 时的 turn 同一口径。
func (a *Agent) LastTurn() uint32 {
	if a.Core == nil {
		return 0
	}
	es := a.Core.Snapshot().Entries
	if len(es) == 0 {
		return 0
	}
	return es[len(es)-1].Turn
}

// trimMessage 截到 store.LastEndMessageMax 字节以内（按 UTF-8 字符边界，截断时末尾加「…」）。
func trimMessage(s string) string {
	const ellipsis = "…"
	if len(s) <= store.LastEndMessageMax {
		return s
	}
	cut := store.LastEndMessageMax - len(ellipsis)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + ellipsis
}
