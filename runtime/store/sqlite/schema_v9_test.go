package sqlite

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// v9 清掉被带 tenant 前缀的索引覆盖的旧索引、没有查询用的 session_entries_turn，
// 会话列表改用 (tenant, updated_at)（2026-11-04 审查 P2 冗余索引）。
func TestV9DropsRedundantIndexes(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "s.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, name := range []string{"session_pending_s", "tool_results_deadline", "trace_events_t", "session_entries_turn", "sessions_list"} {
		var n int
		if err := db.DB().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, name).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("索引 %s 应已删除", name)
		}
	}
	rows, err := db.DB().Query(`EXPLAIN QUERY PLAN SELECT id FROM sessions WHERE tenant = 'local' AND status != 'deleted'
		ORDER BY updated_at DESC LIMIT 50`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	p := strings.Join(plan, "; ")
	if !strings.Contains(p, "sessions_recent") || strings.Contains(p, "TEMP B-TREE") {
		t.Fatalf("会话列表应走 sessions_recent、不排序：%s", p)
	}
}
