package sessions

// C3b（Mortise docs/76 第四版 §5.2、§5.3）：session_send / session_list / session_stop 只沿创建链；
// 第二层只能回复创建者；限流与去重；信封带发件人、caps 为本轮有效权限、按不可信内容投递。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/runtime/callctx"
	"github.com/Sevyn17/tenon/runtime/host"
	"github.com/Sevyn17/tenon/runtime/mail"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
	"github.com/Sevyn17/tenon/runtime/store/memstore"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// mkSession 直接建会话行（带创建链字段）并打开它。
func mkSession(t *testing.T, h *host.Host, db *memstore.Store, meta store.SessionMeta) host.Handle {
	t.Helper()
	ctx := context.Background()
	c := session.New(&session.Config{Model: "m", Policy: session.DefaultPolicy()})
	if err := db.CreateSession(ctx, meta, c.Snapshot().Header, nil); err != nil {
		t.Fatal(err)
	}
	hd, err := h.Open(ctx, meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	return hd
}

func manageCall(t *testing.T, tool *toolhub.Tool, sid string, depth int, args string) toolhub.Output {
	t.Helper()
	kc := callctx.New(callctx.Options{SessionID: sid})
	kc.Bind(callctx.Session{ID: sid, ChainDepth: depth, Definition: "main"}, nil, nil)
	out, err := tool.Call(context.Background(), &toolhub.Cx{SessionID: sid, CallID: "call-m", Tool: tool, Ctx: kc}, json.RawMessage(args))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// chainRig p（第一层）派出 c1、c2（第二层）；q 是链外的另一个顶层会话，q1 由 q 派出。
func chainRig(t *testing.T) (*host.Host, *memstore.Store) {
	t.Helper()
	h, db := chainHost(t)
	mkSession(t, h, db, store.SessionMeta{Workspace: "w", ID: "p"})
	mkSession(t, h, db, store.SessionMeta{Workspace: "w", ID: "q"})
	mkSession(t, h, db, store.SessionMeta{Workspace: "w", ID: "c1", CreatorSession: "p", ChainDepth: 2, ChainRoot: "p"})
	mkSession(t, h, db, store.SessionMeta{ID: "c2", CreatorSession: "p", ChainDepth: 2, ChainRoot: "p", ParentID: "p", Origin: Origin})
	mkSession(t, h, db, store.SessionMeta{Workspace: "w", ID: "q1", CreatorSession: "q", ChainDepth: 2, ChainRoot: "q"})
	return h, db
}

func outboxOf(t *testing.T, h *host.Host, db *memstore.Store, id string) []mail.Envelope {
	t.Helper()
	ctx := context.Background()
	hd, _ := h.Get(id)
	if err := hd.Agent().Commit(ctx); err != nil {
		t.Fatal(err)
	}
	items, err := db.Outbox(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []mail.Envelope
	for _, it := range items {
		var e mail.Envelope
		if json.Unmarshal(it.Envelope, &e) == nil && e.From.ID == id {
			out = append(out, e)
		}
	}
	return out
}

func TestSendOnlyAlongChain(t *testing.T) {
	h, db := chainRig(t)
	send := SendTool(h)
	// 链外：p 投给 q、p 投给 q 派出的 q1。
	for _, to := range []string{"q", "q1"} {
		out := manageCall(t, send, "p", 1, `{"session":"`+to+`","message":"hi"}`)
		if out.Status != session.ToolError || !strings.Contains(contentText(out.Content), "创建链") {
			t.Fatalf("p→%s 应被拒：%+v", to, out)
		}
	}
	// 第二层发给兄弟会话、发给链外：拒绝，并说明只能回复创建者。
	for _, to := range []string{"c2", "q"} {
		out := manageCall(t, send, "c1", 2, `{"session":"`+to+`","message":"hi"}`)
		if out.Status != session.ToolError || !strings.Contains(contentText(out.Content), "只能回复创建你的会话") {
			t.Fatalf("第二层 c1→%s 应被拒：%+v", to, out)
		}
	}
	// 回复创建者成功；派出方投给自己派出的会话成功。
	if out := manageCall(t, send, "c1", 2, `{"session":"p","message":"做完了"}`); out.Status != session.ToolOK {
		t.Fatalf("c1 回复创建者应成功：%+v", out)
	}
	if out := manageCall(t, send, "p", 1, `{"session":"c1","message":"补充一点","mode":"context"}`); out.Status != session.ToolOK {
		t.Fatalf("p→c1 应成功：%+v", out)
	}
	// 发件人标记、不可信、caps 为本轮有效权限（非 nil，收件方求交不借道升级）。
	envs := outboxOf(t, h, db, "c1")
	if len(envs) != 1 {
		t.Fatalf("c1 发件箱应有 1 封：%+v", envs)
	}
	e := envs[0]
	if e.To.ID != "p" || e.From.Kind != mail.AddrSession || e.From.ID != "c1" || e.Origin != mail.OriginExternal || e.Caps == nil ||
		!strings.Contains(e.Body, "来自会话 session:c1（main）") || !strings.Contains(e.Body, "不是用户指令") || e.Mode != mail.ModeWake {
		t.Fatalf("信封：%+v", e)
	}
	pe := outboxOf(t, h, db, "p")
	if len(pe) != 1 || pe[0].Mode != mail.ModeNotice || pe[0].Kind != mail.KindNotify {
		t.Fatalf("context 模式应是 notice：%+v", pe)
	}
	// 派发后收件方按不可信内容入信箱。
	ctx := context.Background()
	if _, err := h.FlushOutbox(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "c1 收到 notice", func() bool {
		l, _ := db.Load(ctx, "c1", nil)
		for _, en := range l.Snapshot.Entries {
			if en.Item.Injection != nil && strings.Contains(en.Item.Injection.Text, "补充一点") {
				return en.Meta[session.MetaTrust] == "untrusted"
			}
		}
		return false
	})
}

// caps：发件方这一轮被收窄到某个工具集时，信封带的就是这个集合（收件方本轮只能与之求交）。
func TestSendCarriesSenderCaps(t *testing.T) {
	h, db := chainRig(t)
	hd, _ := h.Get("p")
	hd.Agent().SetCaps([]string{})
	if out := manageCall(t, SendTool(h), "p", 1, `{"session":"c1","message":"去做"}`); out.Status != session.ToolOK {
		t.Fatalf("%+v", out)
	}
	envs := outboxOf(t, h, db, "p")
	if len(envs) != 1 || envs[0].Caps == nil || len(envs[0].Caps) != 0 {
		t.Fatalf("发件方 caps 为空集时信封也应是空集（不能变成不限）：%+v", envs)
	}
	if envs[0].Corr == "" || envs[0].Kind != mail.KindRequest {
		t.Fatalf("派出方的 wake 信应带 corr 以便回投：%+v", envs[0])
	}
}

func TestSendLimiter(t *testing.T) {
	now := time.Unix(1000, 0)
	l := &limiter{now: func() time.Time { return now }}
	for i := 0; i < SendPerTurn; i++ {
		if why := l.allow("p", 1, "c1", string(rune('a'+i))); why != "" {
			t.Fatalf("第 %d 封不应被限：%s", i+1, why)
		}
	}
	if why := l.allow("p", 1, "c1", "z"); !strings.Contains(why, "达到上限") {
		t.Fatalf("超过每回合上限应拒绝：%q", why)
	}
	if why := l.allow("p", 2, "c1", "z"); why != "" {
		t.Fatalf("下一回合重新计数：%q", why)
	}
	if why := l.allow("p", 2, "c1", "z"); !strings.Contains(why, "没有重复投递") {
		t.Fatalf("60 秒内同内容应去重：%q", why)
	}
	now = now.Add(SendDedupe)
	if why := l.allow("p", 3, "c1", "z"); why != "" {
		t.Fatalf("过了去重窗口应放行：%q", why)
	}
	if got := cutBytes(strings.Repeat("汉", 10), 7); got != "汉汉" {
		t.Fatalf("截断不应切断 UTF-8：%q", got)
	}
}

func TestListAndStopOnlyOwn(t *testing.T) {
	h, _ := chainRig(t)
	out := manageCall(t, ListTool(h), "p", 1, `{}`)
	txt := contentText(out.Content)
	if out.Status != session.ToolOK || !strings.Contains(txt, "c1") || !strings.Contains(txt, "c2") || strings.Contains(txt, "q1") ||
		!strings.Contains(txt, "派出的会话 2 个") || !strings.Contains(txt, "子会话") || !strings.Contains(txt, "独立会话") {
		t.Fatalf("p 只列自己派出的：%s", txt)
	}
	out = manageCall(t, ListTool(h), "c1", 2, `{"include_creator":true}`)
	if txt := contentText(out.Content); !strings.Contains(txt, "创建本会话的会话：p") || !strings.Contains(txt, "没有派出的会话") {
		t.Fatalf("c1：%s", txt)
	}
	stop := StopTool(h)
	if out := manageCall(t, stop, "p", 1, `{"session":"q1"}`); out.Status != session.ToolError {
		t.Fatalf("p 不能停 q1：%+v", out)
	}
	if out := manageCall(t, stop, "c1", 2, `{"session":"p"}`); out.Status != session.ToolError {
		t.Fatalf("c1 不能停创建者：%+v", out)
	}
	if out := manageCall(t, stop, "p", 1, `{"session":"c1"}`); out.Status != session.ToolOK {
		t.Fatalf("p 停 c1 应成功：%+v", out)
	}
}
