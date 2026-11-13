package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/runtime/store/memstore"
)

// span 随 checkpoint 落库：一次工具回合至少有两个 attempt span 和一个 tool span。
// 存储实现已迁出框架，这里读内存存储里的 span，不查表。
func TestSpansCommittedWithCheckpoint(t *testing.T) {
	db := tempStore(t)
	a := newAgent(t, db, agentOpts{})
	if _, err := a.Send(context.Background(), "ping"); err != nil {
		t.Fatal(err)
	}
	if len(a.buf.spans) != 0 {
		t.Fatalf("回合结束后不应有未提交 span: %d", len(a.buf.spans))
	}
	ms, ok := db.(*memstore.Store)
	if !ok {
		t.Fatal("tempStore 应为 memstore")
	}
	counts := map[string]int{}
	ttft := 0
	for _, sp := range ms.SpansForTest("s1") {
		counts[sp.Kind]++
		if sp.Kind == "attempt" && strings.Contains(string(sp.Attrs), "ttft_ms") {
			ttft++
		}
	}
	if counts["attempt"] < 2 || counts["tool"] != 1 {
		t.Fatalf("span 计数不符: %v", counts)
	}
	if ttft == 0 {
		t.Fatal("attempt span 应记首字延迟")
	}
}
