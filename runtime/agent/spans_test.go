package agent

import (
	"context"
	"testing"
)

// span 随 checkpoint 落库：一次工具回合至少有两个 attempt span 和一个 tool span。
func TestSpansCommittedWithCheckpoint(t *testing.T) {
	db := tempStore(t)
	a := newAgent(t, db, agentOpts{})
	if _, err := a.Send(context.Background(), "ping"); err != nil {
		t.Fatal(err)
	}
	if len(a.buf.spans) != 0 {
		t.Fatalf("回合结束后不应有未提交 span: %d", len(a.buf.spans))
	}
	counts := map[string]int{}
	rows, err := db.DB().Query(`SELECT kind FROM trace_spans WHERE session_id = 's1'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		_ = rows.Scan(&k)
		counts[k]++
	}
	if counts["attempt"] < 2 || counts["tool"] != 1 {
		t.Fatalf("span 计数不符: %v", counts)
	}
	var ttft int
	_ = db.DB().QueryRow(`SELECT count(*) FROM trace_spans WHERE kind='attempt' AND attrs LIKE '%ttft_ms%'`).Scan(&ttft)
	if ttft == 0 {
		t.Fatal("attempt span 应记首字延迟")
	}
}
