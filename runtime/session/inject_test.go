package session

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// joinText 取出消息里全部文本片段，便于断言渲染结果。
func joinText(c Content) string {
	var b strings.Builder
	for _, p := range c {
		if p.Kind == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

func TestInjectAppendsEntryAndRendersAsUserMessage(t *testing.T) {
	h := newHarness(t, 8)
	if _, err := h.core.Input(txt("你好"), map[string]any{MetaSource: "user"}); err != nil {
		t.Fatalf("Input: %v", err)
	}
	status := "<status-update>\n<item source=\"todo\" key=\"todo\">任务 1</item>\n</status-update>"
	if err := h.core.Inject(KindStatus, status, map[string]any{
		"status":   map[string]string{"todo": "h1"},
		MetaSource: "kernel",
		MetaTrust:  "trusted",
	}); err != nil {
		t.Fatalf("Inject status: %v", err)
	}
	snap := "<context source=\"instructions\" key=\"agents_md\">AGENTS 基线</context>"
	if err := h.core.Inject(KindSnapshot, snap, nil); err != nil {
		t.Fatalf("Inject snapshot: %v", err)
	}
	ctx := h.core.Context()
	last := ctx.Items[len(ctx.Items)-1]
	if last.Item.Kind != KindSnapshot || last.Item.Injection == nil || last.Item.Injection.Text != snap {
		t.Fatalf("最后一条应为 snapshot 注入：%+v", last.Item)
	}
	req, err := BuildRequest(ctx)
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	var texts []string
	for _, m := range req.Messages {
		texts = append(texts, joinText(m.Content))
	}
	joined := strings.Join(texts, "\n")
	if !strings.Contains(joined, "任务 1") || !strings.Contains(joined, "AGENTS 基线") {
		t.Fatalf("请求缺注入内容：\n%s", joined)
	}
	// 注入条目渲染成 user 消息（不能进 system，12.5）。
	lastMsg := req.Messages[len(req.Messages)-1]
	if lastMsg.Role != "user" || joinText(lastMsg.Content) != snap {
		t.Fatalf("末条消息应为 user 注入：%+v", lastMsg)
	}
}

func TestInjectRejectsUnknownKind(t *testing.T) {
	h := newHarness(t, 8)
	if err := h.core.Inject("profile", "x", nil); !errors.Is(err, ErrState) {
		t.Fatalf("未知注入种类应报 ErrState：%v", err)
	}
	if err := h.core.Inject(KindStatus, "   ", nil); err != nil {
		t.Fatalf("空体应静默跳过：%v", err)
	}
}

func TestInjectRestoreRoundTrip(t *testing.T) {
	h := newHarness(t, 8)
	status := "<status-update><item key=\"todo\">任务</item></status-update>"
	if err := h.core.Inject(KindStatus, status, map[string]any{
		"status": map[string]string{"todo": "h1"},
	}); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	// 走一遍 JSON round trip（checkpoint 落库路径）。
	data, err := json.Marshal(h.core.Snapshot())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var back Snapshot
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	c2, err := Restore(back)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	items := c2.Context().Items
	last := items[len(items)-1]
	if last.Item.Kind != KindStatus || last.Item.Injection == nil || last.Item.Injection.Text != status {
		t.Fatalf("恢复后注入条目不符：%+v", last.Item)
	}
	// 已发哈希（meta.status）也要保留，重启后不重复注入（B16 修订）。
	meta, ok := last.Meta["status"].(map[string]any)
	if !ok || meta["todo"] != "h1" {
		t.Fatalf("恢复后 meta.status 丢失：%+v", last.Meta)
	}
}

func TestRestoreRejectsInjectionWithoutBody(t *testing.T) {
	h := newHarness(t, 8)
	snap := h.core.Snapshot()
	snap.Header.LogLen++
	snap.Entries = append(snap.Entries, &Entry{ID: "bad", Item: Item{Kind: KindStatus}})
	if _, err := Restore(snap); !errors.Is(err, ErrState) {
		t.Fatalf("缺体注入应被 Restore 拒绝：%v", err)
	}
}
