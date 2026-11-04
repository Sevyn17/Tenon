package agent

import (
	"testing"

	"ybase/runtime/toolhub"
)

// 子调用（<父>#<n>）的 span 挂在父调用下，普通调用挂在步骤下。
func TestHubSpanParent(t *testing.T) {
	a := &Agent{stepID: "step-1"}
	a.HubSpan(toolhub.SpanEvent{CallID: "c1", Tool: "run_code"})
	a.HubSpan(toolhub.SpanEvent{CallID: "c1#1", Tool: "read_file"})
	if len(a.hubSpans) != 2 {
		t.Fatalf("spans = %d", len(a.hubSpans))
	}
	if p := a.hubSpans[0].Parent; p != "step-1" {
		t.Fatalf("父调用 parent = %q", p)
	}
	if p := a.hubSpans[1].Parent; p != "tool-c1" {
		t.Fatalf("子调用 parent = %q", p)
	}
}
