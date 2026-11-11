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
