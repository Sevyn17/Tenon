package sessions

// 会话回顾（Mortise docs/76 第四版 §16，C3a）：session_turns 列回合或按关键词搜条目，session_read 按条目 id / 回合 / 调用读原文。
// 只读；结果作为工具结果追加在日志末尾，不改写已发送的历史（不伤前缀缓存）。两个工具从会话开始就在清单里。
// 读取范围：本会话，以及本会话派出的会话（creator_session == 本会话）。第二层会话只能读自己（不能读创建者，D4）。

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/Sevyn17/tenon/runtime/host"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// 工具名。
const (
	TurnsToolName = "session_turns"
	ReadToolName  = "session_read"
)

// 回合状态（docs/76 §16.1）。
const (
	TurnInContext = "in_context"
	TurnCompacted = "compacted"
	TurnPartial   = "partial"
	TurnCurrent   = "current"
	TurnRewound   = "rewound"
)

// 输出上限（docs/76 §16.3）：单次缺省 8 KB、最大 32 KB，超出分段；列表每页缺省 50、最多 200；搜索最多 20 条、片段约 200 字。
const (
	ReadDefaultBytes = 8 << 10
	ReadMaxBytes     = 32 << 10
	TurnsDefaultPage = 50
	TurnsMaxPage     = 200
	SearchMaxHits    = 20
	SnippetRunes     = 200
	lineRunes        = 80
)

// view 被读会话的条目与压缩边界。
type view struct {
	id      string
	entries []*session.Entry // 完整日志 [0, LogLen)
	base    int              // 生效的压缩边界：头部 CompactionBase，回合内有暂存压缩时取其 upto（该步请求已按它发出）
	current uint32           // 进行中的回合（0 = 无）
}

// resolveTarget 校验读取范围并取被读会话。target 空 = 本会话。
func resolveTarget(ctx context.Context, h *host.Host, cx *toolhub.Cx, target string) (*view, string) {
	self := cx.SessionID
	id := strings.TrimSpace(target)
	if id == "" {
		id = self
	}
	l, err := host.LoadHead(ctx, h.DB, id)
	if err != nil {
		if id == self {
			return nil, fmt.Sprintf("读取本会话失败：%v", err)
		}
		return nil, fmt.Sprintf("找不到会话 %s", id)
	}
	if id != self && l.Meta.CreatorSession != self {
		return nil, fmt.Sprintf("只能读本会话和本会话派出的会话；%s 不是本会话派出的", id)
	}
	hdr := l.Snapshot.Header
	if id == self { // 本会话：用最近一次提交的头部（含暂存压缩），存储头部可能晚一步
		if hd, ok := h.Get(id); ok && hd.Agent() != nil {
			if s := hd.Agent().Committed(); s.Header.LogLen >= hdr.LogLen {
				hdr = s.Header
			}
		}
	}
	es, err := h.DB.Entries(ctx, id, 0, -1)
	if err != nil {
		return nil, fmt.Sprintf("读取条目失败：%v", err)
	}
	v := &view{id: id, entries: es, base: hdr.CompactionBase}
	if pc := hdr.PendingCompact; pc != nil && pc.Upto > v.base {
		v.base = pc.Upto
	}
	if hdr.Turn != nil {
		v.current = hdr.Turn.Turn
	}
	return v, ""
}

// turnRow 一个回合的摘要行。
type turnRow struct {
	turn            uint32
	rewindSeq       int
	from, to        int // 完整日志下标 [from, to)；归档为原下标
	firstID         string
	user, assistant string
	tools           []string
	tokens          int
	status          string
	inContextFrom   string // partial：从这个条目起还在上下文里
}

// countable 进回合摘要的条目种类（压缩条目的回合号是压缩发生时的回合，不算进被压缩的回合）。
func countable(e *session.Entry) bool {
	switch e.Item.Kind {
	case session.KindUser, session.KindAssistant, session.KindToolResult:
		return true
	}
	return false
}

// groupTurns 按回合聚合条目（idxOf 给出条目在完整日志里的下标）。
func groupTurns(es []*session.Entry, idxOf func(i int) int) []*turnRow {
	var rows []*turnRow
	byTurn := map[uint32]*turnRow{}
	for i, e := range es {
		if !countable(e) {
			continue
		}
		idx := idxOf(i)
		r := byTurn[e.Turn]
		if r == nil {
			r = &turnRow{turn: e.Turn, from: idx, to: idx + 1, firstID: e.ID}
			byTurn[e.Turn] = r
			rows = append(rows, r)
		}
		if idx < r.from {
			r.from, r.firstID = idx, e.ID
		}
		if idx+1 > r.to {
			r.to = idx + 1
		}
		txt := store.EntryText(e)
		r.tokens += (len(txt) + 3) / 4
		switch e.Item.Kind {
		case session.KindUser:
			if r.user == "" {
				r.user = firstLine(txt, lineRunes)
			}
		case session.KindAssistant:
			if a := e.Item.Assistant; a != nil {
				for _, c := range a.ToolCalls {
					r.tools = append(r.tools, c.Name)
				}
				if s := firstLine(reviewText(a.Content), lineRunes); s != "" {
					r.assistant = s // 留最后一条有正文的助手回复
				}
			}
		}
	}
	return rows
}

// status 回合状态（docs/76 §16.1）：整段在边界前 compacted，整段在边界后 in_context，跨边界 partial；进行中的回合 current。
func (v *view) status(r *turnRow) {
	switch {
	case v.current != 0 && r.turn == v.current:
		r.status = TurnCurrent
	case r.to <= v.base:
		r.status = TurnCompacted
	case r.from >= v.base:
		r.status = TurnInContext
	default:
		r.status = TurnPartial
		if v.base < len(v.entries) {
			r.inContextFrom = v.entries[v.base].ID
		}
	}
}

func (v *view) liveTurns() []*turnRow {
	rows := groupTurns(v.entries, func(i int) int { return i })
	for _, r := range rows {
		v.status(r)
	}
	return rows
}

func archived(ctx context.Context, db host.Store, id string) ([]store.ArchivedEntry, bool, error) {
	ar, ok := any(db).(store.ArchiveReader)
	if !ok {
		return nil, false, nil
	}
	es, err := ar.ArchivedEntries(ctx, id)
	return es, true, err
}

func rewoundTurns(as []store.ArchivedEntry) []*turnRow {
	var rows []*turnRow
	bySeq := map[int][]store.ArchivedEntry{}
	var seqs []int
	for _, a := range as {
		if _, ok := bySeq[a.RewindSeq]; !ok {
			seqs = append(seqs, a.RewindSeq)
		}
		bySeq[a.RewindSeq] = append(bySeq[a.RewindSeq], a)
	}
	sort.Ints(seqs)
	for _, s := range seqs {
		grp := bySeq[s]
		es := make([]*session.Entry, len(grp))
		for i, a := range grp {
			es[i] = a.Entry
		}
		for _, r := range groupTurns(es, func(i int) int { return grp[i].Idx }) {
			r.rewindSeq, r.status = s, TurnRewound
			rows = append(rows, r)
		}
	}
	return rows
}

// TurnsTool session_turns：列回合（带状态）或按关键词搜条目。
func TurnsTool(h *host.Host) *toolhub.Tool {
	yes := true
	return &toolhub.Tool{
		Spec: session.ToolSpec{Name: TurnsToolName,
			Description: "列出会话的回合（回合号、条目 id、首行、用过的工具、状态：in_context 还在上下文、compacted 已压缩、partial 部分压缩），" +
				"或用 query 按关键词搜。压缩后、用户提到早先内容、拿不准之前说过什么时用；先列或搜，再用 session_read 读原文。",
			Schema: json.RawMessage(`{"type":"object","properties":{` +
				`"session":{"type":"string","description":"会话 id；缺省为本会话，也可以是本会话派出的会话"},` +
				`"query":{"type":"string","description":"关键词；给了就只返回命中的条目片段（最多 20 条，新的在前）"},` +
				`"from":{"type":"integer","description":"从这个回合号起列"},` +
				`"limit":{"type":"integer","description":"最多列多少回合，缺省 50"},` +
				`"include_rewound":{"type":"boolean","description":"也列已回退的回合"}}}`)},
		Risk:     toolhub.Read,
		Parallel: &yes,
		Items:    []string{ItemRead},
		Call: func(ctx context.Context, cx *toolhub.Cx, raw json.RawMessage) (toolhub.Output, error) {
			var a struct {
				Session, Query string
				From, Limit    int
				IncludeRewound bool `json:"include_rewound"`
			}
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return toolhub.Failf("参数格式不对：%v", err), nil
				}
			}
			v, msg := resolveTarget(ctx, h, cx, a.Session)
			if msg != "" {
				return toolhub.Failf("%s", msg), nil
			}
			if q := strings.TrimSpace(a.Query); q != "" {
				return search(ctx, h, v, q)
			}
			limit := a.Limit
			if limit <= 0 {
				limit = TurnsDefaultPage
			}
			if limit > TurnsMaxPage {
				limit = TurnsMaxPage
			}
			rows := v.liveTurns()
			if a.IncludeRewound {
				as, _, err := archived(ctx, h.DB, v.id)
				if err != nil {
					return toolhub.Failf("读取已回退条目失败：%v", err), nil
				}
				rows = append(rows, rewoundTurns(as)...)
			}
			var b strings.Builder
			fmt.Fprintf(&b, "会话 %s：共 %d 个回合", v.id, len(v.liveTurns()))
			if v.base > 0 {
				fmt.Fprintf(&b, "，日志下标 %d 之前已压缩", v.base)
			}
			b.WriteString("\n")
			n, more := 0, false
			for _, r := range rows {
				if int(r.turn) < a.From {
					continue
				}
				if n >= limit {
					more = true
					break
				}
				n++
				writeTurn(&b, r)
			}
			if more {
				b.WriteString("（还有更多回合：用 from 继续）\n")
			}
			if n == 0 {
				b.WriteString("（没有回合）\n")
			}
			return toolhub.Text(wrapUntrusted(b.String())), nil
		},
	}
}

func writeTurn(b *strings.Builder, r *turnRow) {
	fmt.Fprintf(b, "- 回合 %d [%s]", r.turn, r.status)
	if r.status == TurnRewound {
		fmt.Fprintf(b, " 回退序号 %d", r.rewindSeq)
	}
	fmt.Fprintf(b, " 条目 %s ~%d tokens", r.firstID, r.tokens)
	if r.inContextFrom != "" {
		fmt.Fprintf(b, "（从条目 %s 起还在上下文里）", r.inContextFrom)
	}
	b.WriteString("\n")
	if r.user != "" {
		fmt.Fprintf(b, "  用户：%s\n", r.user)
	}
	if len(r.tools) > 0 {
		fmt.Fprintf(b, "  工具：%s\n", strings.Join(dedupe(r.tools), ", "))
	}
	if r.assistant != "" {
		fmt.Fprintf(b, "  助手：%s\n", r.assistant)
	}
}

// search 关键词搜三类条目：存储实现 EntrySearcher 时用它，否则扫已读到的条目。
func search(ctx context.Context, h *host.Host, v *view, q string) (toolhub.Output, error) {
	var hits []store.EntryHit
	if s, ok := any(h.DB).(store.EntrySearcher); ok {
		got, err := s.SearchEntries(ctx, v.id, q, nil, SearchMaxHits*3)
		if err != nil {
			return toolhub.Failf("搜索失败：%v", err), nil
		}
		hits = got
	} else {
		for i := len(v.entries) - 1; i >= 0; i-- {
			if countable(v.entries[i]) {
				hits = append(hits, store.EntryHit{Idx: i, Entry: v.entries[i]})
			}
		}
	}
	rows := v.liveTurns()
	statusOf := map[uint32]*turnRow{}
	for _, r := range rows {
		statusOf[r.turn] = r
	}
	lq := strings.ToLower(q)
	var b strings.Builder
	n := 0
	for _, hit := range hits {
		txt := store.EntryText(hit.Entry)
		pos := strings.Index(strings.ToLower(txt), lq)
		if pos < 0 { // 存储匹配可以偏宽，这里按可读文本核一次
			continue
		}
		st := TurnInContext
		if r := statusOf[hit.Entry.Turn]; r != nil {
			st = r.status
			if st == TurnPartial {
				st = TurnInContext
				if hit.Idx < v.base {
					st = TurnCompacted
				}
			}
		}
		fmt.Fprintf(&b, "- 回合 %d [%s] %s 条目 %s\n  …%s…\n", hit.Entry.Turn, st, kindName(hit.Entry.Item.Kind), hit.Entry.ID, snippet(txt, pos, len(q)))
		if n++; n >= SearchMaxHits {
			break
		}
	}
	if n == 0 {
		return toolhub.Text(fmt.Sprintf("会话 %s 里没有找到 %q。可以换个说法，或用 session_turns 不带 query 浏览回合。", v.id, q)), nil
	}
	return toolhub.Text(wrapUntrusted(fmt.Sprintf("会话 %s 里 %q 的命中（新的在前，共 %d 条）：\n", v.id, q, n) + b.String())), nil
}

// ReadTool session_read：按条目 id、回合或工具调用读原文，可按类型筛选，超出上限分段。
func ReadTool(h *host.Host) *toolhub.Tool {
	yes := true
	return &toolhub.Tool{
		Spec: session.ToolSpec{Name: ReadToolName,
			Description: "读会话里某个条目、某个回合或某次工具调用的原文（entry / turn / call 三选一，id 从 session_turns 取）。" +
				"kinds 只读用户、助手或工具内容。内容已在上下文里就不必读；长内容按 from 分段继续。",
			Schema: json.RawMessage(`{"type":"object","properties":{` +
				`"session":{"type":"string","description":"会话 id；缺省为本会话"},` +
				`"entry":{"type":"string","description":"条目 id（优先用它定位）"},` +
				`"turn":{"type":"integer","description":"回合号"},` +
				`"rewind_seq":{"type":"integer","description":"读已回退的回合时给回退序号"},` +
				`"call":{"type":"string","description":"工具调用 id：读这次调用的参数与结果"},` +
				`"kinds":{"type":"array","items":{"type":"string","enum":["user","assistant","tool"]}},` +
				`"from":{"type":"integer","description":"从第几个字节继续（分段时用）"},` +
				`"max_bytes":{"type":"integer","description":"本次最多返回多少字节，缺省 8192，最大 32768"}}}`)},
		Risk:     toolhub.Read,
		Parallel: &yes,
		Items:    []string{ItemRead},
		Call: func(ctx context.Context, cx *toolhub.Cx, raw json.RawMessage) (toolhub.Output, error) {
			var a struct {
				Session, Entry, Call string
				Turn                 uint32
				RewindSeq            *int `json:"rewind_seq"`
				Kinds                []string
				From                 int
				MaxBytes             int `json:"max_bytes"`
			}
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return toolhub.Failf("参数格式不对：%v", err), nil
				}
			}
			if a.Entry == "" && a.Call == "" && a.Turn == 0 {
				return toolhub.Failf("给 entry、turn 或 call 其中一个（可先用 session_turns 找）"), nil
			}
			v, msg := resolveTarget(ctx, h, cx, a.Session)
			if msg != "" {
				return toolhub.Failf("%s", msg), nil
			}
			kinds, msg := kindSet(a.Kinds)
			if msg != "" {
				return toolhub.Failf("%s", msg), nil
			}
			picked, note, err := pick(ctx, h, v, a.Entry, a.Call, a.Turn, a.RewindSeq)
			if err != nil {
				return toolhub.Failf("读取失败：%v", err), nil
			}
			if len(picked) == 0 {
				return toolhub.Failf("%s", note), nil
			}
			var b strings.Builder
			if note != "" {
				b.WriteString(note + "\n")
			}
			for _, p := range picked {
				if !kinds[p.e.Item.Kind] {
					continue
				}
				st := p.status
				if st == "" {
					st = TurnInContext
					if p.idx < v.base {
						st = TurnCompacted
					}
				}
				fmt.Fprintf(&b, "[回合 %d %s %s 条目 %s]\n%s\n\n", p.e.Turn, kindName(p.e.Item.Kind), st, p.e.ID, renderEntry(p.e))
			}
			return toolhub.Text(segment(b.String(), a.From, a.MaxBytes)), nil
		},
	}
}

type picked struct {
	e      *session.Entry
	idx    int
	status string // 空 = 按下标算；rewound = 已回退
}

// pick 按 entry / call / turn 取条目（当前日志找不到时查回退归档）。
func pick(ctx context.Context, h *host.Host, v *view, entry, call string, turn uint32, rewindSeq *int) ([]picked, string, error) {
	switch {
	case entry != "":
		for i, e := range v.entries {
			if e.ID == entry {
				return []picked{{e: e, idx: i}}, "", nil
			}
		}
		as, _, err := archived(ctx, h.DB, v.id)
		if err != nil {
			return nil, "", err
		}
		for _, a := range as {
			if a.Entry.ID == entry {
				return []picked{{e: a.Entry, idx: a.Idx, status: TurnRewound}}, "（这条已被回退，内容来自归档）", nil
			}
		}
		return nil, fmt.Sprintf("会话 %s 里没有条目 %s", v.id, entry), nil
	case call != "":
		var out []picked
		for i, e := range v.entries {
			if a := e.Item.Assistant; a != nil {
				for _, c := range a.ToolCalls {
					if c.ID == call {
						out = append(out, picked{e: &session.Entry{ID: e.ID, Turn: e.Turn, Item: session.Item{Kind: session.KindAssistant,
							Assistant: &session.AssistantItem{ToolCalls: []session.ToolCall{c}}}}, idx: i})
					}
				}
			}
			if r := e.Item.ToolResult; r != nil && r.CallID == call {
				out = append(out, picked{e: e, idx: i})
			}
		}
		if len(out) == 0 {
			return nil, fmt.Sprintf("会话 %s 里没有工具调用 %s", v.id, call), nil
		}
		return out, "", nil
	default:
		if rewindSeq != nil {
			as, ok, err := archived(ctx, h.DB, v.id)
			if err != nil {
				return nil, "", err
			}
			if !ok {
				return nil, "这个存储读不到已回退的条目", nil
			}
			var out []picked
			for _, a := range as {
				if a.RewindSeq == *rewindSeq && a.Entry.Turn == turn && countable(a.Entry) {
					out = append(out, picked{e: a.Entry, idx: a.Idx, status: TurnRewound})
				}
			}
			if len(out) == 0 {
				return nil, fmt.Sprintf("回退序号 %d 里没有回合 %d", *rewindSeq, turn), nil
			}
			return out, "（已回退的回合，内容来自归档）", nil
		}
		var out []picked
		for i, e := range v.entries {
			if e.Turn == turn && countable(e) {
				out = append(out, picked{e: e, idx: i})
			}
		}
		if len(out) == 0 {
			return nil, fmt.Sprintf("会话 %s 当前日志里没有回合 %d（已回退的回合要带 rewind_seq）", v.id, turn), nil
		}
		return out, "", nil
	}
}

func kindSet(ks []string) (map[string]bool, string) {
	all := map[string]bool{session.KindUser: true, session.KindAssistant: true, session.KindToolResult: true}
	if len(ks) == 0 {
		return all, ""
	}
	out := map[string]bool{}
	for _, k := range ks {
		switch strings.TrimSpace(k) {
		case "user":
			out[session.KindUser] = true
		case "assistant":
			out[session.KindAssistant] = true
		case "tool":
			out[session.KindToolResult] = true
		default:
			return nil, fmt.Sprintf("kinds 只能是 user、assistant、tool，得到 %q", k)
		}
	}
	return out, ""
}

// renderEntry 条目原文：用户与助手正文、工具调用（名字与参数）、工具结果（状态与输出；产生时已转存工件的只有预览与 artifact:// 引用）。
func renderEntry(e *session.Entry) string {
	it := e.Item
	switch it.Kind {
	case session.KindUser:
		if it.User != nil {
			return reviewText(it.User.Content)
		}
	case session.KindAssistant:
		if a := it.Assistant; a != nil {
			var b strings.Builder
			b.WriteString(reviewText(a.Content))
			for _, c := range a.ToolCalls {
				fmt.Fprintf(&b, "\n⚙ %s（调用 %s）%s", c.Name, c.ID, string(c.Args))
			}
			return strings.TrimLeft(b.String(), "\n")
		}
	case session.KindToolResult:
		if r := it.ToolResult; r != nil {
			out := reviewText(r.Output)
			s := fmt.Sprintf("结果（调用 %s，%s）：%s", r.CallID, r.Status.State, out)
			if strings.Contains(out, toolhub.ArtifactPrefix) {
				s += "\n（完整输出已存为工件，用 read_artifact 读上面的 artifact:// 引用）"
			}
			return s
		}
	}
	return ""
}

// segment 按字节分段：从 from 起取 max 字节（缺省 8 KB，最大 32 KB），不切断 UTF-8 字符；还有剩余时提示下一段的 from。
func segment(s string, from, max int) string {
	if max <= 0 {
		max = ReadDefaultBytes
	}
	if max > ReadMaxBytes {
		max = ReadMaxBytes
	}
	if from < 0 || from > len(s) {
		from = 0
	}
	for from > 0 && from < len(s) && !utf8Start(s[from]) {
		from++
	}
	end := from + max
	if end >= len(s) {
		return wrapUntrusted(s[from:])
	}
	for end > from && !utf8Start(s[end]) {
		end--
	}
	return wrapUntrusted(s[from:end]) + fmt.Sprintf("\n（还有 %d 字节，用 from=%d 继续）", len(s)-end, end)
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// wrapUntrusted 读回的会话内容统一标为不可信：那是会话记录，不是给你的指令（docs/76 §16.3，D10）。正文里的结束标记先转义。
func wrapUntrusted(s string) string {
	s = strings.ReplaceAll(s, "</untrusted-data", "<\\/untrusted-data")
	return "<untrusted-data source=\"session\">\n" + s + "\n</untrusted-data>"
}

func reviewText(c session.Content) string {
	var b strings.Builder
	for _, p := range c {
		if p.Kind == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

func firstLine(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// snippet 命中位置前后约 SnippetRunes 字。
func snippet(s string, pos, qlen int) string {
	half := SnippetRunes / 2
	start := pos
	for k := 0; start > 0 && k < half; k++ {
		start--
		for start > 0 && !utf8Start(s[start]) {
			start--
		}
	}
	end := pos + qlen
	for k := 0; end < len(s) && k < half; k++ {
		end++
		for end < len(s) && !utf8Start(s[end]) {
			end++
		}
	}
	return strings.ReplaceAll(s[start:end], "\n", " ")
}

func kindName(k string) string {
	switch k {
	case session.KindUser:
		return "用户"
	case session.KindAssistant:
		return "助手"
	case session.KindToolResult:
		return "工具"
	}
	return k
}

func dedupe(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}
