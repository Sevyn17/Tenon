package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Sevyn17/tenon/runtime/store/memstore"
)

// 压缩成本（Mortise docs/76 C3c）：压缩后第一次 attempt 记 after_compact 指回 compact span，之后的 attempt 不再带。
func TestAttemptAfterCompactLinksSpan(t *testing.T) {
	db := tempStore(t)
	m := &recordModel{}
	a := newAgent(t, db, agentOpts{model: m})
	a.Compactor = &fakeCompactor{}
	if _, err := a.Send(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Send(context.Background(), "again"); err != nil {
		t.Fatal(err)
	}
	ms := db.(*memstore.Store)
	compactID, linked, attempts := "", 0, 0
	for _, sp := range ms.SpansForTest("s1") {
		if sp.Kind == "compact" && sp.Status == "ok" {
			compactID = sp.ID
		}
	}
	if compactID == "" {
		t.Fatal("应有成功的 compact span")
	}
	for _, sp := range ms.SpansForTest("s1") {
		if sp.Kind != "attempt" {
			continue
		}
		attempts++
		var at map[string]any
		_ = json.Unmarshal(sp.Attrs, &at)
		if v, ok := at["after_compact"]; ok {
			if v != compactID {
				t.Fatalf("after_compact=%v 应为 %s", v, compactID)
			}
			linked++
		}
	}
	if linked != 1 || attempts < 2 {
		t.Fatalf("只有压缩后第一次 attempt 关联：linked=%d attempts=%d", linked, attempts)
	}
}
