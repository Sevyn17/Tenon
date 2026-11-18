package store

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/Sevyn17/tenon/runtime/session"
)

// —— 会话列表：预览、查询、归档（Mortise 客户端改进 M7，协议T9、T12）——//

// PreviewMax 会话行预览的上限（rune）：超过截断并补「…」。
const PreviewMax = 120

// Preview 算会话列表一行的预览：从末尾往前找最后一条有正文的 assistant 条目（只有工具调用的跳过），
// 没有就取最后一条有正文的 user 条目；折叠空白，截到 PreviewMax。都没有返回空串（存储把空串当「不变」）。
// 运行时每次检查点用它算 Checkpoint.Preview；sqlite 升级时用它给旧会话回填，两边结果一致。
func Preview(entries []*session.Entry) string {
	user := ""
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e == nil {
			continue
		}
		switch it := e.Item; it.Kind {
		case session.KindAssistant:
			if it.Assistant != nil {
				if s := collapse(textOf(it.Assistant.Content)); s != "" {
					return clipRunes(s, PreviewMax)
				}
			}
		case session.KindUser:
			if user == "" && it.User != nil {
				user = collapse(textOf(it.User.Content))
			}
		}
	}
	return clipRunes(user, PreviewMax)
}

// textOf 内容里的文本片段按顺序拼起来（媒体等忽略）。
func textOf(c session.Content) string {
	var b strings.Builder
	for _, p := range c {
		if p.Kind == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

func clipRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// MetaArchived 会话行业务键里的归档标记（session.update {archived}）：值为 true 表示已归档，没有这个键 = 未归档。
const MetaArchived = "archived"

// ArchivedOf 业务键里是否已归档（只认 JSON true）。
func ArchivedOf(meta json.RawMessage) bool {
	var m map[string]any
	_ = json.Unmarshal(meta, &m)
	return m[MetaArchived] == true
}

// ArchivedFilter 列表怎么对待已归档的会话。
type ArchivedFilter int

const (
	ArchivedExclude ArchivedFilter = iota // 排除已归档（缺省）
	ArchivedOnly                          // 只要已归档
	ArchivedInclude                       // 都要
)

// SessionQuery 会话列表的查询条件（各条件之间是「且」）。
type SessionQuery struct {
	// Workspace 非空：只列属于它的会话。Unassigned 为真时忽略。
	Workspace string
	// Unassigned 只列 workspace 为空的会话（同 UnassignedLister）。
	Unassigned bool
	// Before > 0：只列 updated_at < Before 的（分页游标）。
	Before int64
	// Limit 每页条数；<= 0 或 > 500 按 50。
	Limit int
	// TopOnly 只列顶层会话（parent 为空）。
	TopOnly bool
	// Parent 非空：只列它的直接子会话。
	Parent string
	// Text 非空：标题或任一条 user 条目的正文包含它（不区分 ASCII 大小写）。用户条目只比较前 200 个字符
	// （折叠空白后；sqlite 比较的是 session_entries.summary）。Text 里的 % _ \ 按字面匹配。
	Text string
	// Archived 已归档的会话怎么处理（MetaArchived）。
	Archived ArchivedFilter
}

// SessionQueryStore 可选：按条件列会话（文本搜索、按 parent、归档）。客户端改进 M7 协议T12。
// 与 UnassignedLister 一样不加进 Store：第三方存储不实现也照常工作，只是没有这些筛选（hello 不报 sessions.query）。
type SessionQueryStore interface {
	// QuerySessions 按更新时间倒序列出满足 q 的未删除会话，最多 q.Limit 条。
	QuerySessions(ctx context.Context, q SessionQuery) ([]SessionInfo, error)
}

// SearchSummaryMax 文本搜索比较用户条目正文的长度上限（rune），与 sqlite 的 session_entries.summary 一致。
const SearchSummaryMax = 200

// ContainsFold s 是否包含 sub，只折叠 ASCII 大小写（与 sqlite LIKE 的缺省行为一致；非 ASCII 字符按原样比较）。
func ContainsFold(s, sub string) bool {
	return strings.Contains(asciiLower(s), asciiLower(sub))
}

func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// SearchText user 条目用来做文本搜索的正文：文本片段拼接、折叠空白、截到 SearchSummaryMax（不补省略号）。
// memstore 用它；sqlite 比较 summary 列（同样的折叠与截断，截断时多一个「…」，不影响包含判断）。
func SearchText(c session.Content) string {
	s := collapse(textOf(c))
	if r := []rune(s); len(r) > SearchSummaryMax {
		return string(r[:SearchSummaryMax])
	}
	return s
}
