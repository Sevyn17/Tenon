package host

// C3c（Mortise docs/76 §16）：回投与 session_send 的 notice 落成 status 条目时，meta.replies 记来源会话与条目 id；
// 回投正文截到约 1 KB、不切 UTF-8。

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Sevyn17/tenon/runtime/mail"
	"github.com/Sevyn17/tenon/runtime/session"
)

func TestNoticeRepliesMeta(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, nil, &fakeModel{})
	hd, err := r.h.Open(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	admitEnvelope(t, r.db, "p", mail.Envelope{ID: "rp-c1", From: mail.Address{Kind: mail.AddrSession, ID: "ag-1"},
		To: mail.Address{Kind: mail.AddrSession, ID: "p"}, Kind: mail.KindNotify, Mode: mail.ModeNotice,
		Caps: []string{}, Body: "[会话 session:ag-1（main）这一轮结束：done；条目 e-42]\n做完了"})
	admitEnvelope(t, r.db, "p", mail.Envelope{ID: "sd-1", From: mail.Address{Kind: mail.AddrSession, ID: "ag-2"},
		To: mail.Address{Kind: mail.AddrSession, ID: "p"}, Kind: mail.KindNotify, Mode: mail.ModeNotice,
		Caps: []string{}, Body: "顺便说一句"})
	if err := hd.Do(Command{Kind: Deliver}); err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	var st *session.Entry
	for _, e := range hd.Agent().Committed().Entries {
		if e.Item.Kind == session.KindStatus {
			st = e
		}
	}
	if st == nil {
		t.Fatal("应写一条 status 条目")
	}
	if st.Meta["trust"] != "untrusted" {
		t.Fatalf("来自会话的 notice 应不可信：%v", st.Meta)
	}
	reps, _ := st.Meta[MetaReplies].([]any)
	if len(reps) != 2 {
		t.Fatalf("replies=%v", st.Meta[MetaReplies])
	}
	a, _ := reps[0].(map[string]any)
	b, _ := reps[1].(map[string]any)
	if a["from"] != "ag-1" || a["entry"] != "e-42" || b["from"] != "ag-2" || b["send"] != true {
		t.Fatalf("replies 内容不符：%v / %v", a, b)
	}
}

func TestReplyTruncateAndHeader(t *testing.T) {
	long := strings.Repeat("回", 1000) // 3000 字节
	got := truncateBytes(long, ReplyMaxBytes)
	if len(got) > ReplyMaxBytes+len("…") || !utf8.ValidString(got) {
		t.Fatalf("截断应 ≤1KB 且不切 UTF-8：%d", len(got))
	}
	if ReplyMaxBytes != 1024 {
		t.Fatalf("ReplyMaxBytes=%d", ReplyMaxBytes)
	}
	if e := replyHeaderEntry("[会话 session:x（m）这一轮结束：done；条目 abc]\nbody"); e != "abc" {
		t.Fatalf("取条目 id：%q", e)
	}
	if e := replyHeaderEntry("没有头"); e != "" {
		t.Fatalf("无头应为空：%q", e)
	}
	es := []*session.Entry{
		{ID: "u1", Item: session.Item{Kind: session.KindUser}},
		{ID: "a1", Item: session.Item{Kind: session.KindAssistant, Assistant: &session.AssistantItem{Content: session.Content{session.NewTextPart("早")}}}},
		{ID: "a2", Item: session.Item{Kind: session.KindAssistant, Assistant: &session.AssistantItem{}}},
	}
	if txt, id := lastAssistantEntry(es); txt != "早" || id != "a1" {
		t.Fatalf("最后一条有文本的助手条目：%q %q", txt, id)
	}
}
