package appapi

// 翻译层固定用例（06 10.3）：每种内核条目各一条，翻译成线上形状后与 testdata/wire/entries.json 逐字比对。
// 内核类型改字段时这里应当不变；有意改线上形状时用 `go test ./appapi -run TestWireEntriesGolden -update` 重录，
// 同一份用例给前端 client 的状态合成测试用。

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ybase/appapi/wire"
	"ybase/runtime/session"
)

var updateWire = flag.Bool("update", false, "重录 testdata/wire 固定用例")

func wireFixtureEntries() []*session.Entry {
	think := "先看文件"
	return []*session.Entry{
		{ID: "e0", Turn: 1, At: 1000, Meta: map[string]any{session.MetaSource: "user", MetaClientIDs: []any{"c-1", "c-2"}},
			Item: session.Item{Kind: session.KindUser, User: &session.UserItem{Content: session.Content{
				session.NewTextPart("看图"), {Kind: "media", Media: &session.MediaRef{URI: "artifact://abc", MIME: "image/png", Bytes: 3, SHA256: "abc"}}}}}},
		{ID: "e1", Turn: 1, At: 1001, Item: session.Item{Kind: session.KindAssistant, Assistant: &session.AssistantItem{
			Reasoning: []session.ReasoningBlock{{Text: &think, Opaque: map[string]any{"signature": "不上线"}}},
			Content:   session.Content{session.NewTextPart("好")},
			ToolCalls: []session.ToolCall{{ID: "c1", WireID: "toolu_1", Name: "read_file", Args: json.RawMessage(`{"path":"a.go"}`)},
				{ID: "c2", Name: "glob"}},
			Model: "m", Usage: session.Usage{Input: 10, Output: 2, CacheRead: 8, Source: "Reported"}}}},
		{ID: "e2", Turn: 1, At: 1002, Item: session.Item{Kind: session.KindToolResult, ToolResult: &session.ToolResultItem{
			CallID: "c1", Output: session.Content{session.NewTextPart("package a")}, Status: session.ToolOK}}},
		{ID: "e3", Turn: 1, At: 1003, Item: session.Item{Kind: session.KindToolResult, ToolResult: &session.ToolResultItem{
			CallID: "c2", Status: session.ToolTurnCancelled}}},
		{ID: "e4", Turn: 2, At: 1004, Item: session.Item{Kind: session.KindCompaction, Compaction: &session.CompactionItem{
			Upto: 3, Summary: session.Content{session.NewTextPart("摘要")}}}},
		{ID: "e5", Turn: 2, At: 1005, Meta: map[string]any{"status": map[string]any{"todo": "hash"}},
			Item: session.Item{Kind: session.KindStatus, Injection: &session.InjectionItem{Text: "<status-update/>"}}},
		{ID: "e6", Turn: 2, At: 1006, Item: session.Item{Kind: session.KindUnknown, Unknown: &session.UnknownItem{Raw: json.RawMessage(`{"kind":"future"}`)}}},
	}
}

func TestWireEntriesGolden(t *testing.T) {
	var buf bytes.Buffer
	for i, e := range wireFixtureEntries() {
		b, err := json.Marshal(WireEntry(i, e, nil))
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	path := filepath.Join("testdata", "wire", "entries.jsonl")
	if *updateWire {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("缺固定用例（先用 -update 录一次）：%v", err)
	}
	if got := buf.String(); got != strings.ReplaceAll(string(want), "\r\n", "\n") {
		t.Fatalf("线上形状与固定用例不同（有意变化用 -update 重录）：\n%s", got)
	}
}

// 线上不出现只对模型适配器有意义的字段，键名都是小写下划线（G6）。
func TestWireEntryHidesKernelFields(t *testing.T) {
	for i, e := range wireFixtureEntries() {
		b, _ := json.Marshal(WireEntry(i, e, nil))
		s := string(b)
		for _, bad := range []string{"toolu_1", "signature", "WireID", "Opaque", "CallID", `"hash"`, "meta"} {
			if strings.Contains(s, bad) {
				t.Errorf("条目 %d 不应带 %s：%s", i, bad, s)
			}
		}
	}
}

// 工具结果超过 MaxWireText 时截断；能存全文时给 output_ref。
func TestWireToolOutputClipped(t *testing.T) {
	e := &session.Entry{ID: "e", Item: session.Item{Kind: session.KindToolResult, ToolResult: &session.ToolResultItem{
		CallID: "c", Output: session.Content{session.NewTextPart(strings.Repeat("中", MaxWireText))}, Status: session.ToolOK}}}
	var stored string
	w := WireEntry(0, e, func(text string) (string, bool) { stored = text; return "artifact://full", true })
	if !w.Item.Truncated || w.Item.OutputRef != "artifact://full" || stored == "" {
		t.Fatalf("应截断并给引用：%+v", w.Item)
	}
	n := 0
	for _, p := range w.Item.Output {
		n += len(p.Text)
	}
	if n > MaxWireText {
		t.Fatalf("截断后仍有 %d 字节", n)
	}
}

func TestWireTurnEnded(t *testing.T) {
	for _, c := range []struct {
		end  session.EndReason
		want wire.TurnEnded
	}{
		{session.DoneReason(), wire.TurnEnded{Turn: 3, Kind: "done"}},
		{session.CancelledReason(), wire.TurnEnded{Turn: 3, Kind: "cancelled"}},
		{session.MaxStepsReason(), wire.TurnEnded{Turn: 3, Kind: "max_steps"}},
		{session.EndReason{Kind: session.EndFailed, Code: "RATE_LIMIT", Message: "慢点"}, wire.TurnEnded{Turn: 3, Kind: "failed", Code: "RATE_LIMIT", Message: "慢点"}},
	} {
		if got := WireTurnEnded(3, c.end); got != c.want {
			t.Errorf("%v → %+v，期望 %+v", c.end, got, c.want)
		}
	}
}
