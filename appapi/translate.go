package appapi

// 翻译层（protocol 2，06 10.3）：内核类型 → 线上类型（appapi/wire）。内核字段改名、结构调整只改这里，
// 线上形状不变。规则：
//   - 键名一律小写下划线（G6），item 平铺成判别联合，snapshot / status 合并成 context；
//   - 只对模型适配器有意义的字段（WireID、Reasoning.Opaque、meta 里的哈希表）不上线；
//   - 工具结果文本超过 MaxWireText 时截断，带 truncated 与 output_ref（全文用 artifact.get 取）；
//     截断只发生在这里，内核日志不变。

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/runtime/session"
)

// MaxWireText 线上工具结果文本的上限（06 10.12 Q6）；超出截断并给 output_ref。
const MaxWireText = 64 << 10

// MetaClientIDs 用户条目 meta 里记吃进的输入的 client_id（inbox item id，06 10.3）。
const MetaClientIDs = session.MetaClientIDs

// Spill 把超长的工具结果全文存成工件，返回引用；nil 或失败时只截断、不给引用。
type Spill func(text string) (ref string, ok bool)

// WireEntry 把一条内核条目翻译成线上条目。idx 是它在完整日志里的下标。
func WireEntry(idx int, e *session.Entry, spill Spill) wire.Entry {
	out := wire.Entry{ID: e.ID, Idx: idx, Turn: e.Turn, At: e.At}
	if s, ok := e.Meta[session.MetaSource].(string); ok {
		out.Source = s
	}
	out.ClientIDs = stringList(e.Meta[MetaClientIDs])
	out.Item = wireItem(e.Item, spill)
	return out
}

func wireItem(it session.Item, spill Spill) wire.Item {
	switch it.Kind {
	case session.KindUser:
		if it.User != nil {
			return wire.Item{Kind: wire.KindUser, Content: wireParts(it.User.Content)}
		}
	case session.KindAssistant:
		if a := it.Assistant; a != nil {
			w := wire.Item{Kind: wire.KindAssistant, Content: wireParts(a.Content), Model: a.Model,
				Interrupted: a.Interrupted}
			for _, r := range a.Reasoning {
				if r.Text != nil && *r.Text != "" {
					w.Reasoning = append(w.Reasoning, wire.Reasoning{Text: *r.Text})
				}
			}
			for _, c := range a.ToolCalls {
				w.ToolCalls = append(w.ToolCalls, wire.ToolCall{ID: c.ID, Name: c.Name, Args: jsonValue(c.Args)})
			}
			if u := a.Usage; u != (session.Usage{}) {
				w.Usage = &wire.Usage{Input: u.Input, Output: u.Output, CacheRead: u.CacheRead,
					CacheWrite: u.CacheWrite, Source: usageSource(u.Source)}
			}
			return w
		}
	case session.KindToolResult:
		if r := it.ToolResult; r != nil {
			w := wire.Item{Kind: wire.KindToolResult, CallID: r.CallID, Status: r.Status.State,
				CancelReason: cancelReason(r.Status.CancelReason)}
			w.Output, w.Truncated, w.OutputRef = clipOutput(r.Output, spill)
			return w
		}
	case session.KindCompaction:
		if c := it.Compaction; c != nil {
			return wire.Item{Kind: wire.KindCompaction, Upto: c.Upto, Summary: wireParts(c.Summary)}
		}
	case session.KindSnapshot, session.KindStatus:
		w := wire.Item{Kind: wire.KindContext, ContextSource: it.Kind}
		if it.Injection != nil {
			w.Text = it.Injection.Text
		}
		return w
	}
	raw, _ := json.Marshal(it)
	if it.Unknown != nil && len(it.Unknown.Raw) > 0 {
		raw = it.Unknown.Raw
	}
	return wire.Item{Kind: wire.KindUnknown, Raw: raw}
}

func wireParts(c session.Content) []wire.Part {
	if len(c) == 0 {
		return nil
	}
	out := make([]wire.Part, 0, len(c))
	for _, p := range c {
		w := wire.Part{Kind: p.Kind, Text: p.Text, Data: p.Data}
		if m := p.Media; m != nil {
			w.Media = &wire.MediaRef{Ref: m.URI, MIME: m.MIME, Size: m.Bytes, SHA256: m.SHA256}
		}
		if p.Kind == "data" {
			if d, ok := p.Data.(map[string]any); ok {
				w.Schema, _ = d["schema"].(string)
			}
		}
		out = append(out, w)
	}
	return out
}

// clipOutput 工具结果的文本部件合计超过 MaxWireText 时截断（按 UTF-8 边界）；能存全文时给引用。
func clipOutput(c session.Content, spill Spill) ([]wire.Part, bool, string) {
	parts := wireParts(c)
	total := 0
	for _, p := range parts {
		total += len(p.Text)
	}
	if total <= MaxWireText {
		return parts, false, ""
	}
	ref := ""
	if spill != nil {
		var full strings.Builder
		for _, p := range c {
			full.WriteString(p.Text)
		}
		if r, ok := spill(full.String()); ok {
			ref = r
		}
	}
	left := MaxWireText
	for i := range parts {
		t := parts[i].Text
		if len(t) <= left {
			left -= len(t)
			continue
		}
		cut := left
		for cut > 0 && !utf8.RuneStart(t[cut]) {
			cut--
		}
		parts[i].Text = t[:cut]
		left = 0
	}
	return parts, true, ref
}

// jsonValue 工具参数原样作为 JSON 值上线；空参数写 {}，不合法的 JSON 包成字符串（不让整帧编码失败）。
func jsonValue(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	if json.Valid(raw) {
		return raw
	}
	b, _ := json.Marshal(string(raw))
	return b
}

func usageSource(s string) string {
	switch strings.ToLower(s) {
	case "reported":
		return "reported"
	case "estimated":
		return "estimated"
	default:
		return "missing"
	}
}

func cancelReason(s string) string {
	switch s {
	case "":
		return ""
	case "TurnCancelled":
		return "turn_cancelled"
	case "Redirected":
		return "redirected"
	default:
		return strings.ToLower(s)
	}
}

// WireRunState 内核状态 → 线上运行状态：只有 idle / running（06 10.4）。
func WireRunState(s session.Status) string {
	if s == session.StatusIdle {
		return wire.RunIdle
	}
	return wire.RunRunning
}

// WireTurnEnded 回合结束原因结构化（G7）。
func WireTurnEnded(turn uint32, end session.EndReason) wire.TurnEnded {
	out := wire.TurnEnded{Turn: turn, Code: end.Code, Message: end.Message}
	switch end.Kind {
	case session.EndDone:
		out.Kind = "done"
	case session.EndCancelled:
		out.Kind = "cancelled"
	case session.EndMaxSteps:
		out.Kind = "max_steps"
	case session.EndFailed:
		out.Kind = "failed"
	default:
		out.Kind = "failed"
	}
	return out
}

func stringList(v any) []string {
	switch s := v.(type) {
	case []string:
		return append([]string(nil), s...)
	case []any:
		out := make([]string, 0, len(s))
		for _, x := range s {
			if t, ok := x.(string); ok {
				out = append(out, t)
			}
		}
		return out
	case string:
		if s != "" {
			return []string{s}
		}
	}
	return nil
}
