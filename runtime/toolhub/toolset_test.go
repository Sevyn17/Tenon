package toolhub

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/runtime/session"
)

// 可用集与显露集（tool-hub D1）。
//
// 2026-11-13（L12）：延迟显露机制删除后，显露集恒等于可用集（减去档位判 deny 的）。
// 原用例 TestToolsetDeferAndSearch 测的是「超预算把 Deferred 工具移出显露集、tool_search
// 追加回来」那条链，已随机制一起删；这里保留仍有意义的两条：全量显露、可用集外不得执行。

func specNames(ss []session.ToolSpec) string {
	var n []string
	for _, s := range ss {
		n = append(n, s.Name)
	}
	return strings.Join(n, ",")
}

// 工具少时全量显露，没有隐藏项。
func TestToolsetExposesAll(t *testing.T) {
	h := New(Options{})
	ok := func(context.Context, *Cx, json.RawMessage) (Output, error) { return Text("ok"), nil }
	_ = h.Register(&Tool{Spec: spec("read"), Call: ok})
	_ = h.Register(&Tool{Spec: spec("gh_issue"), Call: ok})
	_ = h.Register(&Tool{Spec: spec("secret"), Call: ok})

	ts := h.SpecsFor([]string{"read", "gh_issue"})
	if got := specNames(h.Exposed(ts)); got != "read,gh_issue" {
		t.Fatalf("应全量显露可用集: %q", got)
	}
	if got := strings.Join(ts.Available(), ","); got != "read,gh_issue" {
		t.Fatalf("可用集: %q", got)
	}
}

// 可用集外的工具不得执行（这条与延迟机制无关，是可用集本身的语义）。
func TestToolsetRejectsOutsideAvailable(t *testing.T) {
	h := New(Options{})
	ok := func(context.Context, *Cx, json.RawMessage) (Output, error) { return Text("ok"), nil }
	_ = h.Register(&Tool{Spec: spec("read"), Call: ok})
	_ = h.Register(&Tool{Spec: spec("secret"), Call: ok})

	ts := h.SpecsFor([]string{"read"})
	rs := drain(h.ExecuteIn(context.Background(), ts, kctx("s"), []session.ToolCall{
		call("1", "secret", `{}`),
	}, nil))
	if rs[0].Output.Status != session.ToolError {
		t.Fatal("可用集外的工具不得执行")
	}
}
