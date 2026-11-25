package sessions

// 会话回顾（Mortise docs/76 第四版 §16，C3a）：回合状态、关键词搜索、按条目 id 分类读、读取范围、分段。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/runtime/host"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
	"github.com/Sevyn17/tenon/runtime/store/memstore"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

func rtxt(s string) session.Content { return session.Content{session.NewTextPart(s)} }

// reviewRig 建会话 id：回合文本依次为 texts，提交到 db；compactUpto > 0 时在最后压缩到该下标。
func reviewRig(t *testing.T, db *memstore.Store, meta store.SessionMeta, texts []string, compactUpto int) *session.Core {
	t.Helper()
	ctx := context.Background()
	c := session.New(&session.Config{Model: "m", Policy: session.DefaultPolicy()})
	if err := db.CreateSession(ctx, meta, c.Snapshot().Header, nil); err != nil {
		t.Fatal(err)
	}
	for _, s := range texts {
		n, err := c.Input(rtxt(s), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.ModelDone(n.Model.RequestID, n.ID, session.AssistantItem{Content: rtxt("答复：" + s)}, session.StopReason{V: "stop"}); err != nil {
			t.Fatal(err)
		}
	}
	if compactUpto > 0 {
		if err := c.Compact(rtxt("摘要"), compactUpto, nil); err != nil {
			t.Fatal(err)
		}
	}
	s := c.Snapshot()
	if err := db.Commit(ctx, store.Checkpoint{SessionID: meta.ID, ExpectLogLen: 0, AppendFrom: 0,
		Append: s.Entries, Header: s.Header, RunState: "idle"}); err != nil {
		t.Fatal(err)
	}
	return c
}

func callTool(t *testing.T, tool *toolhub.Tool, sid, args string) toolhub.Output {
	t.Helper()
	out, err := tool.Call(context.Background(), &toolhub.Cx{SessionID: sid, CallID: "call-1", Tool: tool}, json.RawMessage(args))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestTurnsStatusAndSearch(t *testing.T) {
	db := memstore.New()
	t.Cleanup(func() { db.Close() })
	h := &host.Host{DB: db}
	// 三回合各 2 条（下标 0–5），压缩到 4：回合 1、2 已压缩，回合 3 还在上下文里。
	reviewRig(t, db, store.SessionMeta{ID: "s"}, []string{"部署脚本报错", "换成蓝绿发布", "继续"}, 4)
	out := callTool(t, TurnsTool(h), "s", `{}`)
	txt := contentText(out.Content)
	if out.Status.State != "ok" {
		t.Fatalf("列回合失败：%s", txt)
	}
	for _, want := range []string{"回合 1", "回合 3", TurnCompacted, TurnInContext, "<untrusted-data"} {
		if !strings.Contains(txt, want) {
			t.Fatalf("回合列表缺 %q：\n%s", want, txt)
		}
	}
	out = callTool(t, TurnsTool(h), "s", `{"query":"蓝绿"}`)
	txt = contentText(out.Content)
	if !strings.Contains(txt, "蓝绿发布") || !strings.Contains(txt, TurnCompacted) {
		t.Fatalf("搜索应命中已压缩回合的片段：\n%s", txt)
	}
	if strings.Contains(callToolText(t, h, "s", `{"query":"不存在的词"}`), "蓝绿") {
		t.Fatal("没命中时不应返回片段")
	}
}

func callToolText(t *testing.T, h *host.Host, sid, args string) string {
	return contentText(callTool(t, TurnsTool(h), sid, args).Content)
}

func TestReadByEntryKindsAndScope(t *testing.T) {
	db := memstore.New()
	t.Cleanup(func() { db.Close() })
	h := &host.Host{DB: db}
	c := reviewRig(t, db, store.SessionMeta{ID: "p"}, []string{"第一个问题", "第二个问题"}, 0)
	es := c.Snapshot().Entries
	// 按条目 id 读，只要用户消息。
	out := callTool(t, ReadTool(h), "p", `{"entry":"`+es[0].ID+`","kinds":["user"]}`)
	txt := contentText(out.Content)
	if !strings.Contains(txt, "第一个问题") || strings.Contains(txt, "答复：第一个问题") || !strings.Contains(txt, "<untrusted-data") {
		t.Fatalf("按条目读用户消息：\n%s", txt)
	}
	// 按回合读助手回复。
	txt = contentText(callTool(t, ReadTool(h), "p", `{"turn":2,"kinds":["assistant"]}`).Content)
	if !strings.Contains(txt, "答复：第二个问题") || strings.Contains(txt, "第二个问题\n") && !strings.Contains(txt, "答复") {
		t.Fatalf("按回合读助手回复：\n%s", txt)
	}
	// 派出的会话能读；别人的会话、创建者都不能读（第二层不能读创建者）。
	reviewRig(t, db, store.SessionMeta{ID: "p/sub-1", ParentID: "p", CreatorSession: "p", ChainDepth: 2}, []string{"子任务"}, 0)
	reviewRig(t, db, store.SessionMeta{ID: "other"}, []string{"别人的"}, 0)
	if out := callTool(t, TurnsTool(h), "p", `{"session":"p/sub-1"}`); out.Status.State != "ok" {
		t.Fatalf("应能读本会话派出的会话：%s", contentText(out.Content))
	}
	if out := callTool(t, TurnsTool(h), "p", `{"session":"other"}`); out.Status.State == "ok" {
		t.Fatal("不应能读别的会话")
	}
	if out := callTool(t, ReadTool(h), "p/sub-1", `{"session":"p","turn":1}`); out.Status.State == "ok" {
		t.Fatal("第二层不应能读创建者")
	}
}

func TestReadSegments(t *testing.T) {
	db := memstore.New()
	t.Cleanup(func() { db.Close() })
	h := &host.Host{DB: db}
	long := strings.Repeat("长文本", 4000) // 约 36 KB
	reviewRig(t, db, store.SessionMeta{ID: "s"}, []string{long}, 0)
	txt := contentText(callTool(t, ReadTool(h), "s", `{"turn":1,"kinds":["user"]}`).Content)
	if len(txt) > ReadDefaultBytes+512 || !strings.Contains(txt, "from=") {
		t.Fatalf("超出缺省上限应分段并提示 from：len=%d", len(txt))
	}
}

func TestRewoundTurnsListedOnRequest(t *testing.T) {
	db := memstore.New()
	t.Cleanup(func() { db.Close() })
	h := &host.Host{DB: db}
	ctx := context.Background()
	c := reviewRig(t, db, store.SessionMeta{ID: "s"}, []string{"一", "二"}, 0)
	to := 2
	if err := c.Rewind(to); err != nil {
		t.Fatal(err)
	}
	s := c.Snapshot()
	if err := db.Commit(ctx, store.Checkpoint{SessionID: "s", ExpectLogLen: 4, TruncateFrom: &to, AppendFrom: to,
		Header: s.Header, RunState: "idle"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(callToolText(t, h, "s", `{}`), TurnRewound) {
		t.Fatal("缺省不应列已回退的回合")
	}
	if txt := callToolText(t, h, "s", `{"include_rewound":true}`); !strings.Contains(txt, TurnRewound) {
		t.Fatalf("include_rewound 应列出已回退回合：\n%s", txt)
	}
}
