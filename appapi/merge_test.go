package appapi

import (
	"testing"

	"github.com/Sevyn17/tenon/runtime/agent"
)

// MergeHooks 要并入全部钩子：漏掉 OnStream 时应用自己设了钩子（如 CLI）就收不到 protocol 2 的分段流式事件。
func TestMergeHooksKeepsOnStream(t *testing.T) {
	var a, b int
	h := MergeHooks(agent.Hooks{OnStream: func(agent.StreamEvent) { a++ }}, agent.Hooks{OnStream: func(agent.StreamEvent) { b++ }})
	if h.OnStream == nil {
		t.Fatal("MergeHooks 丢了 OnStream")
	}
	h.OnStream(agent.StreamEvent{Kind: agent.StreamText})
	if a != 1 || b != 1 {
		t.Fatalf("两组 OnStream 都应调用：%d %d", a, b)
	}
}
