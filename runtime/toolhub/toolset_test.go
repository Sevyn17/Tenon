package toolhub

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/runtime/session"
)

// D1：工具少时全量显露、不出现 tool_search；超预算时 Deferred 工具移出显露集，
// tool_search 选中后步边界追加；可用集外的调用按不存在处理。
func TestToolsetDeferAndSearch(t *testing.T) {
	h := New(Options{})
	ok := func(context.Context, *Cx, json.RawMessage) (Output, error) { return Text("ok"), nil }
	_ = h.Register(&Tool{Spec: spec("read"), Call: ok})
	_ = h.Register(&Tool{Spec: session.ToolSpec{Name: "gh_issue", Description: "创建 GitHub issue",
		Schema: json.RawMessage(`{"type":"object"}`)}, Deferred: true, Call: ok})
	_ = h.Register(&Tool{Spec: spec("secret"), Call: ok})

	small := h.SpecsFor([]string{"read", "gh_issue"}, 0)
	names := func(ss []session.ToolSpec) string {
		var n []string
		for _, s := range ss {
			n = append(n, s.Name)
		}
		return strings.Join(n, ",")
	}
	if got := names(h.Exposed(small)); got != "read,gh_issue" || small.Catalog() != "" {
		t.Fatalf("不超预算应全量显露且无 tool_search: %q", got)
	}

	ts := h.SpecsFor([]string{"read", "gh_issue"}, 1)
	if got := names(h.Exposed(ts)); got != "tool_search,read" { // 内置工具先登记
		t.Fatalf("超预算显露集: %q", got)
	}
	if !strings.Contains(ts.Catalog(), "gh_issue") {
		t.Fatalf("目录段缺延迟工具: %q", ts.Catalog())
	}
	rs := drain(h.ExecuteIn(context.Background(), ts, kctx("s"), []session.ToolCall{
		call("1", "tool_search", `{"query":"issue"}`),
		call("2", "secret", `{}`),
	}, nil))
	if !strings.Contains(text(rs[0].Output), "gh_issue") {
		t.Fatalf("tool_search: %q", text(rs[0].Output))
	}
	if rs[1].Output.Status != session.ToolError {
		t.Fatal("可用集外的工具不得执行")
	}
	add := ts.TakePending()
	if len(add) != 1 || add[0].Name != "gh_issue" {
		t.Fatalf("待追加: %+v", add)
	}
	if got := names(h.Exposed(ts)); !strings.Contains(got, "gh_issue") {
		t.Fatalf("追加后显露集: %q", got)
	}
}
