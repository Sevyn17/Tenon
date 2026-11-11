package group

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Sevyn17/tenon/runtime/agent"
	"github.com/Sevyn17/tenon/runtime/callctx"
	"github.com/Sevyn17/tenon/runtime/host"
	"github.com/Sevyn17/tenon/runtime/mail"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
	"github.com/Sevyn17/tenon/runtime/store/sqlite"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// echoModel 回显请求里所有 user + 注入文本，记录下来供断言。
type echoModel struct {
	mu   sync.Mutex
	seen []string
}

func (m *echoModel) Stream(ctx context.Context, req session.Request, onText func(string)) (session.AssistantItem, session.StopReason, error) {
	var last string
	var all []string
	for _, msg := range req.Messages {
		txt := ""
		for _, p := range msg.Content {
			txt += p.Text
		}
		if txt != "" {
			all = append(all, txt)
			last = txt
		}
	}
	m.mu.Lock()
	m.seen = append(m.seen, all...)
	m.mu.Unlock()
	return session.AssistantItem{Content: session.Content{session.NewTextPart("re:" + last)}}, session.StopReason{V: "stop"}, nil
}

func (m *echoModel) has(sub string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.seen {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// rig 用真实 host + sqlite，直接构造 Group（不走组件装配），测 Group 逻辑。
func rig(t *testing.T) (*Group, *host.Host, *sqlite.DB, *echoModel) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "g.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	m := &echoModel{}
	h := &host.Host{DB: db}
	mk := func(id, def string) (*agent.Agent, *session.Config, error) {
		hub := toolhub.New(toolhub.Options{Store: agent.ToolStore{DB: db}})
		a := &agent.Agent{Store: db, SessionID: id, Model: m, Hub: hub}
		return a, &session.Config{Model: "m", Tools: hub.Exposed(hub.SpecsFor(nil, 0)), Policy: session.DefaultPolicy()}, nil
	}
	h.New = mk
	for _, c := range []string{"messages", "reads"} {
		if err := db.SetCollectionVersion(ctx, store.Tenant, "group", c, 1); err != nil {
			t.Fatal(err)
		}
	}
	msgs, err := db.Open(ctx, store.Tenant, "group", "messages", store.CollectionSpec{
		Version: 1, Indexes: []string{"idx"}, Partition: "tenant"})
	if err != nil {
		t.Fatal(err)
	}
	reads, err := db.Open(ctx, store.Tenant, "group", "reads", store.CollectionSpec{
		Version: 1, Indexes: []string{"group"}, Partition: "tenant"})
	if err != nil {
		t.Fatal(err)
	}
	g, err := New(h, db, msgs, reads, nil)
	if err != nil {
		t.Fatal(err)
	}
	return g, h, db, m
}

func cfg() Config {
	return Config{Groups: []GroupConfig{{ID: "g1", Bots: []string{"alice-bot", "bob-bot"}}}}
}

// 人 @alice-bot：只有 alice-bot 被唤醒（有会话），bob-bot 没有会话、没收到信。
func TestMentionWakesOnlyMentioned(t *testing.T) {
	g, h, db, _ := rig(t)
	ctx := context.Background()
	if _, err := g.Post(ctx, cfg(), "g1", mail.Address{Kind: mail.AddrHuman, ID: "张三"}, "@alice-bot 查本周订单"); err != nil {
		t.Fatal(err)
	}
	// alice-bot 的会话应按 key=g1 建出并开回合。
	aid, err := db.FindSessionByKey(ctx, "alice-bot", "", "g1")
	if err != nil {
		t.Fatalf("alice-bot 应有会话：%v", err)
	}
	if hd, ok := h.Get(aid); ok {
		_ = hd.Wait()
	}
	// bob-bot 没有会话（没被 @，不投）。
	if _, err := db.FindSessionByKey(ctx, "bob-bot", "", "g1"); err == nil {
		t.Fatal("bob-bot 不该被唤醒建会话")
	}
}

// A 回复写回群记录；B 后来被 @ 时，背景注入含 A 的回复。
func TestBackgroundIncludesPriorReply(t *testing.T) {
	g, h, db, m := rig(t)
	ctx := context.Background()
	// 张三 @alice-bot
	if _, err := g.Post(ctx, cfg(), "g1", mail.Address{Kind: mail.AddrHuman, ID: "张三"}, "@alice-bot 查本周订单"); err != nil {
		t.Fatal(err)
	}
	aid, _ := db.FindSessionByKey(ctx, "alice-bot", "", "g1")
	if hd, ok := h.Get(aid); ok {
		_ = hd.Wait()
	}
	// alice-bot 回合结束 → 回复写回群记录
	g.AfterTurn(aid)

	// 确认记录里有 alice-bot 的回复（echo 模型回 "re:...")
	recs, err := g.recordsAfter(ctx, "g1", 0)
	if err != nil {
		t.Fatal(err)
	}
	var hasAliceReply bool
	for _, r := range recs {
		if r.FromKind == mail.AddrAgent && strings.HasPrefix(r.Body, "re:") {
			hasAliceReply = true
		}
	}
	if !hasAliceReply {
		t.Fatalf("群记录里应有 alice-bot 的回复：%+v", recs)
	}

	// 李四 @bob-bot
	if _, err := g.Post(ctx, cfg(), "g1", mail.Address{Kind: mail.AddrHuman, ID: "李四"}, "@bob-bot 汇总一下"); err != nil {
		t.Fatal(err)
	}
	bid, _ := db.FindSessionByKey(ctx, "bob-bot", "", "g1")
	if bid == "" {
		t.Fatal("bob-bot 应被唤醒")
	}
	// 手动跑一次 background（真实装配里由 Status 在调模型前触发；rig 里直接调）。
	cx := callctx.New(callctx.Options{SessionID: bid})
	cx.Bind(callctx.Session{ID: bid}, metaRaw("g1", "bob-bot", []string{"alice-bot", "bob-bot"}), nil)
	items := g.Background(cx)
	if len(items) == 0 {
		t.Fatal("bob-bot 背景应注入群记录")
	}
	body := items[0].Body
	if !strings.Contains(body, "re:") {
		t.Fatalf("背景里应含 alice-bot 的回复：\n%s", body)
	}
	if !strings.Contains(body, "@alice-bot 查本周订单") {
		t.Fatalf("背景里应含张三最初的 @ 发言：\n%s", body)
	}
	_ = m
}

// 已读位置能连续推进：第二次 Background 不重复注入已读过的记录（回归：setReadPos 曾用 ifVersion=0，
// 第二次必失败，导致背景每轮重复注入）。
func TestBackgroundAdvancesReadPos(t *testing.T) {
	g, _, _, _ := rig(t)
	ctx := context.Background()
	cfgG := cfg()
	// 两条人的发言（不 @ 任何机器人，避免建会话）。
	if _, err := g.Post(ctx, cfgG, "g1", mail.Address{Kind: mail.AddrHuman, ID: "张三"}, "第一条"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Post(ctx, cfgG, "g1", mail.Address{Kind: mail.AddrHuman, ID: "张三"}, "第二条"); err != nil {
		t.Fatal(err)
	}
	cx := callctx.New(callctx.Options{SessionID: "bot-sess"})
	cx.Bind(callctx.Session{ID: "bot-sess"}, metaRaw("g1", "alice-bot", []string{"alice-bot"}), nil)

	first := g.Background(cx)
	if len(first) == 0 {
		t.Fatal("第一次应注入背景")
	}
	// 已读位置应推进到末条；第二次没有新记录 → 不再注入。
	second := g.Background(cx)
	if len(second) != 0 {
		t.Fatalf("已读位置没推进，背景重复注入：%v", second)
	}
	// 再来一条新记录 → 只注入新的那条。
	if _, err := g.Post(ctx, cfgG, "g1", mail.Address{Kind: mail.AddrHuman, ID: "李四"}, "第三条"); err != nil {
		t.Fatal(err)
	}
	third := g.Background(cx)
	if len(third) == 0 {
		t.Fatal("有新记录应注入")
	}
	if strings.Contains(third[0].Body, "第一条") || strings.Contains(third[0].Body, "第二条") {
		t.Fatalf("只该注入新的那条：%s", third[0].Body)
	}
	if !strings.Contains(third[0].Body, "第三条") {
		t.Fatalf("应注入第三条：%s", third[0].Body)
	}
}
