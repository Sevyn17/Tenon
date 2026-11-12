// Package group 实现多人小组：群记录读写、@ 解析与扇出、每回合背景补齐、
// 机器人回复写回、转人工（框架设计 13，T4e）。
package group

// 小组核心逻辑（T4e）：群记录读写、@ 解析与扇出、每回合背景补齐、机器人回复写回、转人工。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/Sevyn17/tenon/runtime/assemble"
	"github.com/Sevyn17/tenon/runtime/callctx"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/host"
	"github.com/Sevyn17/tenon/runtime/inject"
	"github.com/Sevyn17/tenon/runtime/mail"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
)

// seqWidth 群记录 key 的左补零宽度：字典序=数值序，供分页扫"已读位置之后"。
const seqWidth = 20

// Group 小组服务。
type Group struct {
	Host  *host.Host
	DB    store.Store
	Msgs  store.Collection // 群记录
	Reads store.Collection // 已读位置 + 每群 next_seq（__meta__:<群>）
	UI    assemble.UI

	mu sync.Mutex // 串行化"取 next_seq → 写记录"（同进程内），跨进程靠集合 ifVersion CAS 兜底
}

// New 建小组服务。
func New(h *host.Host, db store.Store, msgs, reads store.Collection, ui assemble.UI) (*Group, error) {
	if h == nil || db == nil || msgs == nil || reads == nil {
		return nil, fmt.Errorf("group: 缺少依赖（host / store / messages / reads）")
	}
	return &Group{Host: h, DB: db, Msgs: msgs, Reads: reads, UI: ui}, nil
}

// ---------- meta ----------

// sessionMeta 机器人会话的业务 meta：群 id、机器人定义名、同群机器成员（供 afterTurn 过滤 @）。
type sessionMeta struct {
	Group   string   `json:"group"`
	Bot     string   `json:"bot"`
	Members []string `json:"members"`
}

// metaRaw 造业务 meta 的 JSON（交给 Host.DeliverAgent 的 bizMeta）。
func metaRaw(groupID, bot string, members []string) json.RawMessage {
	b, _ := json.Marshal(sessionMeta{Group: groupID, Bot: bot, Members: members})
	return b
}

func parseMeta(m map[string]any) (sessionMeta, bool) {
	g, _ := m["group"].(string)
	b, _ := m["bot"].(string)
	if g == "" || b == "" {
		return sessionMeta{}, false
	}
	var members []string
	if list, ok := m["members"].([]any); ok {
		for _, v := range list {
			if s, ok := v.(string); ok {
				members = append(members, s)
			}
		}
	}
	return sessionMeta{Group: g, Bot: b, Members: members}, true
}

// sessionMetaByID 从会话行读业务 meta（afterTurn 只有 id 时用）。
func (g *Group) sessionMetaByID(ctx context.Context, id string) (sessionMeta, bool) {
	l, err := g.DB.Load(ctx, id, nil)
	if err != nil || len(l.Meta.Meta) == 0 {
		return sessionMeta{}, false
	}
	var m map[string]any
	if json.Unmarshal(l.Meta.Meta, &m) != nil {
		return sessionMeta{}, false
	}
	return parseMeta(m)
}

// ---------- 群记录 ----------

// record 一条群记录。
type record struct {
	Seq      int64
	Group    string
	From     string // 发言者地址字符串
	FromKind string
	Body     string
	At       []string
	TS       int64
}

// keyOf seq 的零补 key。
func keyOf(seq int64) string { return fmt.Sprintf("%0*d", seqWidth, seq) }

// renderRecord 资源类型 group 的渲染：谁说的 + 内容。
func renderRecord(rec component.ResourceRecord) string {
	from := rec.From.String()
	if rec.From.Kind == mail.AddrComponent {
		from = rec.From.ID
	}
	return "[" + from + "] " + rec.Body
}

// ---------- 序号 ----------

// nextSeq 取某群下一个序号（CAS 写 __meta__）。并发由 Reads.Put 的 ifVersion 兜底。
func (g *Group) nextSeq(ctx context.Context, groupID string) (int64, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	metaKey := "__meta__:" + groupID
	// 跨进程并发时 CAS 会失败（别的进程刚写过），重试几次取新版本；进程内由 g.mu 串行，不会撞。
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		var cur int64
		var ver int64
		if d, err := g.Reads.Get(ctx, store.Tenant, metaKey); err == nil {
			cur = toInt64(d.Body["next_seq"])
			ver = d.Version
		} else if !errors.Is(err, store.ErrNotFound) {
			return 0, err
		}
		next := cur + 1
		err := g.Reads.Put(ctx, store.Tenant, store.Doc{
			Key: metaKey, Body: map[string]any{"group": groupID, "next_seq": next, "tenant": store.Tenant},
		}, ver)
		if err == nil {
			return next, nil
		}
		if !errors.Is(err, store.ErrConflict) {
			return 0, err
		}
		lastErr = err
	}
	return 0, fmt.Errorf("group: 取序号连续冲突：%w", lastErr)
}

// Append 往群里写一条记录并返回序号。
func (g *Group) Append(ctx context.Context, groupID string, from mail.Address, body string) (int64, error) {
	seq, err := g.nextSeq(ctx, groupID)
	if err != nil {
		return 0, fmt.Errorf("group: 取序号：%w", err)
	}
	r := record{Seq: seq, Group: groupID, From: from.String(), FromKind: from.Kind, Body: body, At: parseMentions(body)}
	if err := g.Msgs.Put(ctx, store.Tenant, store.Doc{Key: keyOf(seq), Body: toBody(r)}, 0); err != nil {
		return 0, err
	}
	return seq, nil
}

func toBody(r record) map[string]any {
	at := make([]any, len(r.At))
	for i, v := range r.At {
		at[i] = v
	}
	return map[string]any{"seq": r.Seq, "group": r.Group, "from": r.From, "from_kind": r.FromKind,
		"body": r.Body, "at": at, "ts": r.TS, "idx": "all", "tenant": store.Tenant}
}

func fromBody(b map[string]any) record {
	str := func(k string) string { s, _ := b[k].(string); return s }
	var at []string
	if list, ok := b["at"].([]any); ok {
		for _, v := range list {
			if s, ok := v.(string); ok {
				at = append(at, s)
			}
		}
	}
	return record{Seq: toInt64(b["seq"]), Group: str("group"), From: str("from"),
		FromKind: str("from_kind"), Body: str("body"), At: at, TS: toInt64(b["ts"])}
}

// recordsAfter 取群 groupID 中 seq > afterSeq 的记录（升序，分页扫 idx="all"）。
func (g *Group) recordsAfter(ctx context.Context, groupID string, afterSeq int64) ([]record, error) {
	var out []record
	after := ""
	if afterSeq > 0 {
		after = keyOf(afterSeq)
	}
	for {
		docs, page, err := g.Msgs.Query(ctx, store.Tenant, "idx", "all", store.Page{After: after, Limit: store.PageLimit(500)})
		if err != nil {
			return nil, err
		}
		for _, d := range docs {
			if r := fromBody(d.Body); r.Group == groupID && r.Seq > afterSeq {
				out = append(out, r)
			}
		}
		if page.After == "" {
			break
		}
		after = page.After
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

// ---------- 已读位置 ----------

func (g *Group) readPos(ctx context.Context, groupID, bot string) int64 {
	d, err := g.Reads.Get(ctx, store.Tenant, bot+"@"+groupID)
	if err != nil {
		return 0
	}
	return toInt64(d.Body["pos"])
}

// setReadPos 推进已读序号（只增，条件写）。
func (g *Group) setReadPos(ctx context.Context, groupID, bot string, pos int64) error {
	key := bot + "@" + groupID
	var ver int64
	if d, err := g.Reads.Get(ctx, store.Tenant, key); err == nil {
		if toInt64(d.Body["pos"]) >= pos {
			return nil // 已读到更后面：不回退
		}
		ver = d.Version
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	return g.Reads.Put(ctx, store.Tenant, store.Doc{
		Key:  key,
		Body: map[string]any{"group": groupID, "bot": bot, "pos": pos, "tenant": store.Tenant},
	}, ver)
}

// ---------- @ 解析 ----------

// parseMentions 取正文里的 @名字（@ 后跟字母数字下划线连字符）。
func parseMentions(body string) []string {
	var out []string
	seen := map[string]bool{}
	for i := 0; i < len(body); i++ {
		if body[i] != '@' {
			continue
		}
		j := i + 1
		for j < len(body) && isNameChar(body[j]) {
			j++
		}
		if j > i+1 {
			if name := body[i+1 : j]; !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
		i = j
	}
	return out
}

func isNameChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
}

// ---------- 投（组件身份，creator=""） ----------

// wakeBot 对被 @ 的机器人投 wake。from=component:group（守卫/联系名单按 component 缺省放行），
// 人的发言写进正文，origin=external（外部不可信）。bizMeta 只在新建会话时写入。
func (g *Group) wakeBot(ctx context.Context, groupID string, members []string, speaker mail.Address, name, body string) error {
	env := mail.Envelope{
		From:   mail.Address{Kind: mail.AddrComponent, ID: ServiceName},
		Via:    ServiceName,
		To:     mail.Address{Kind: mail.AddrAgent, ID: name},
		Kind:   mail.KindRequest,
		Mode:   mail.ModeWake,
		Key:    groupID,
		Origin: mail.OriginExternal,
		Body:   fmt.Sprintf("%s 说：%s", speaker.String(), body),
	}
	_, err := g.Host.DeliverAgent(ctx, env, metaRaw(groupID, name, members))
	return err
}

// wakeMentioned 从一条发言正文里找 @ 的群成员并逐个唤醒。
func (g *Group) wakeMentioned(ctx context.Context, groupID string, members []string, speaker mail.Address, body string) error {
	mem := map[string]bool{}
	for _, b := range members {
		mem[b] = true
	}
	var errs []error
	for _, name := range parseMentions(body) {
		if !mem[name] || name == speaker.ID && speaker.Kind == mail.AddrAgent {
			continue
		}
		if err := g.wakeBot(ctx, groupID, members, speaker, name, body); err != nil {
			errs = append(errs, fmt.Errorf("唤醒 %s：%w", name, err))
		}
	}
	return errors.Join(errs...)
}

// Post 处理一条进群的发言（人 / 渠道 / 转人工）：写群记录，再按 @ 唤醒群里被 @ 的机器人。
// 这是外部往群里送消息的唯一入口；from 是发言者地址（human:<id> 或 component:<渠道>）。
func (g *Group) Post(ctx context.Context, cfg Config, groupID string, from mail.Address, body string) (int64, error) {
	gc := cfg.group(groupID)
	if gc == nil {
		return 0, fmt.Errorf("group: 群 %s 未配置", groupID)
	}
	seq, err := g.Append(ctx, groupID, from, body)
	if err != nil {
		return 0, err
	}
	if err := g.wakeMentioned(ctx, groupID, gc.Bots, from, body); err != nil {
		return seq, err
	}
	return seq, nil
}

// Background 每回合调模型前：本会话若属于某群，把已读位置之后的记录拼成一段背景注入，再推进位置。
func (g *Group) Background(ctx *callctx.Ctx) []inject.Item {
	sm, ok := parseMeta(ctx.Meta())
	if !ok {
		return nil
	}
	bg := context.Background()
	recs, err := g.recordsAfter(bg, sm.Group, g.readPos(bg, sm.Group, sm.Bot))
	if err != nil || len(recs) == 0 {
		return nil
	}
	var b strings.Builder
	var last int64
	for _, r := range recs {
		fmt.Fprintf(&b, "#%d %s\n", r.Seq, renderRecord(component.ResourceRecord{From: mustAddr(r.From), Body: r.Body}))
		last = r.Seq
	}
	_ = g.setReadPos(bg, sm.Group, sm.Bot, last)
	return []inject.Item{{Key: "group_bg", Kind: inject.KindStatus, Body: strings.TrimSpace(b.String()),
		Priority: 10, Trust: inject.TrustExternal}}
}

// ---------- 机器人回复写回 ----------

// AfterTurn 机器人会话回合结束：读本轮最终回复，写回群记录，按 @ 唤醒同群别的机器人。
func (g *Group) AfterTurn(id string) {
	ctx := context.Background()
	sm, ok := g.sessionMetaByID(ctx, id)
	if !ok {
		return
	}
	text, err := g.lastAssistantText(ctx, id)
	if err != nil || strings.TrimSpace(text) == "" {
		return
	}
	from := mail.Address{Kind: mail.AddrAgent, ID: sm.Bot}
	if _, err := g.Append(ctx, sm.Group, from, text); err != nil {
		return
	}
	_ = g.wakeMentioned(ctx, sm.Group, sm.Members, from, text)
}

// lastAssistantText 读会话历史里最后一条 assistant 正文。
func (g *Group) lastAssistantText(ctx context.Context, id string) (string, error) {
	entries, err := g.DB.Entries(ctx, id, 0, -1)
	if err != nil {
		return "", err
	}
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.Item.Kind == session.KindAssistant && e.Item.Assistant != nil {
			var b strings.Builder
			for _, p := range e.Item.Assistant.Content {
				if p.Text != "" {
					b.WriteString(p.Text)
				}
			}
			return b.String(), nil
		}
	}
	return "", nil
}

// ---------- helpers ----------

// group 按群 id 找配置，没有返回 nil。
func (c Config) group(id string) *GroupConfig {
	for i := range c.Groups {
		if c.Groups[i].ID == id {
			return &c.Groups[i]
		}
	}
	return nil
}

func toInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	case string:
		i, _ := strconv.ParseInt(n, 10, 64)
		return i
	}
	return 0
}

func mustAddr(s string) mail.Address {
	a, err := mail.ParseAddress(s)
	if err != nil {
		return mail.Address{ID: s}
	}
	return a
}
