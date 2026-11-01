package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestSessionStats(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "s.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now()
	err = db.tx(ctx, func(tx *sql.Tx) error {
		for i, s := range []Span{
			{ID: "a1", Kind: "attempt", Status: "ok", Attrs: json.RawMessage(`{"input":1000,"output":10,"cache_read":800,"usage_source":"Reported"}`)},
			{ID: "a2", Kind: "attempt", Status: "ok", Attrs: json.RawMessage(`{"input":1000,"output":20,"cache_read":0,"cache_write":5,"usage_source":"Reported"}`)},
			{ID: "a3", Kind: "attempt", Status: "error", Attrs: json.RawMessage(`{"error":"x"}`)},
			{ID: "t1", Kind: "tool", Status: "ok"},
			{ID: "c1", Kind: "compact", Status: "ok"},
		} {
			s.Start = now.Add(time.Duration(i) * time.Millisecond)
			if err := putSpan(ctx, tx, "s", s); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO session_requests(tenant, session_id, request_id, shell_hash, prefix_base, prefix_end, final_hash, composition, created_at)
			VALUES(?,?,?,?,?,?,?,?,?)`, Tenant, "s", "r1", "h", 0, 0, "f", `{"total":5}`, now.UnixMilli())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := db.SessionStats(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	if st.Attempts != 3 || st.Reported != 2 || st.Missing != 1 || st.Errors != 1 || st.ToolCalls != 1 || st.Compacts != 1 {
		t.Fatalf("counts %+v", st)
	}
	if st.Input != 2000 || st.Output != 30 || st.CacheRead != 800 || st.CacheWrite != 5 {
		t.Fatalf("usage %+v", st)
	}
	if r := st.HitRate(); r != 0.4 {
		t.Fatalf("hit rate %v", r)
	}
	if st.LastRequest != "r1" || string(st.Composition) != `{"total":5}` {
		t.Fatalf("last %+v", st)
	}
	empty, err := db.SessionStats(ctx, "none")
	if err != nil || empty.HitRate() != -1 {
		t.Fatalf("empty %+v %v", empty, err)
	}
}
