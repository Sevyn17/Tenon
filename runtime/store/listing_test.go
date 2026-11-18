package store

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/runtime/session"
)

func entry(it session.Item) *session.Entry { return &session.Entry{Item: it} }

func userE(s string) *session.Entry {
	return entry(session.Item{Kind: session.KindUser, User: &session.UserItem{Content: session.Content{session.NewTextPart(s)}}})
}

func asstE(s string, tools ...string) *session.Entry {
	a := &session.AssistantItem{}
	if s != "" {
		a.Content = session.Content{session.NewTextPart(s)}
	}
	for _, n := range tools {
		a.ToolCalls = append(a.ToolCalls, session.ToolCall{ID: n, Name: n, Args: json.RawMessage(`{}`)})
	}
	return entry(session.Item{Kind: session.KindAssistant, Assistant: a})
}

func TestPreview(t *testing.T) {
	tool := entry(session.Item{Kind: session.KindToolResult, ToolResult: &session.ToolResultItem{CallID: "x",
		Output: session.Content{session.NewTextPart("工具输出")}, Status: session.ToolOK}})
	long := strings.Repeat("字", PreviewMax+5)
	cases := []struct {
		name string
		in   []*session.Entry
		want string
	}{
		{"空", nil, ""},
		{"最后一条 assistant", []*session.Entry{userE("问"), asstE("答\n\n 了")}, "答 了"},
		{"纯工具调用跳过", []*session.Entry{userE("问"), asstE("先说"), userE("再问"), asstE("", "read"), tool}, "先说"},
		{"没有 assistant 正文取最后一条 user", []*session.Entry{userE("一"), userE(" 二 "), asstE("", "read"), tool}, "二"},
		{"只有工具结果", []*session.Entry{tool}, ""},
		{"截断", []*session.Entry{asstE(long)}, strings.Repeat("字", PreviewMax) + "…"},
		{"媒体不算正文", []*session.Entry{userE("看图"), entry(session.Item{Kind: session.KindAssistant, Assistant: &session.AssistantItem{
			Content: session.Content{{Kind: "media", Media: &session.MediaRef{URI: "a", MIME: "image/png"}}}}})}, "看图"},
	}
	for _, c := range cases {
		if got := Preview(c.in); got != c.want {
			t.Errorf("%s：%q，应为 %q", c.name, got, c.want)
		}
	}
}

func TestContainsFoldAndSearchText(t *testing.T) {
	if !ContainsFold("Hello World", "wORLD") || ContainsFold("abc", "abd") || !ContainsFold("中文Ab", "文ab") {
		t.Fatal("ASCII 大小写折叠")
	}
	if ContainsFold("ÄB", "äb") { // 与 sqlite LIKE 一致：非 ASCII 不折叠
		t.Fatal("非 ASCII 不应折叠")
	}
	c := session.Content{session.NewTextPart("  a \n b  "), {Kind: "media"}, session.NewTextPart(strings.Repeat("x", 300))}
	got := SearchText(c)
	if !strings.HasPrefix(got, "a b x") || len([]rune(got)) != SearchSummaryMax { // 片段直接拼接，"b  " 后的空白折叠成一个
		t.Fatalf("SearchText %q", got)
	}
}
