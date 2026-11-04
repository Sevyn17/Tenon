package sqlite

import (
	"context"
	"path/filepath"
	"testing"
)

func openTest(t *testing.T) *DB {
	t.Helper()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "state.db"), Options{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestOpenMigratesAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	ctx := context.Background()
	db, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var v int
	if err := db.SQL().QueryRow(`SELECT version FROM schema_versions WHERE owner='core'`).Scan(&v); err != nil || v != len(coreMigrations) {
		t.Fatalf("core 版本 = %d, %v", v, err)
	}
	var mode string
	if err := db.SQL().QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q, %v", mode, err)
	}
	db.Close()

	db2, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatalf("重开: %v", err)
	}
	defer db2.Close()
	for _, tbl := range []string{"sessions", "session_shells", "session_entries", "session_entries_archive",
		"session_requests", "session_inbox", "tool_results", "trace_spans", "blobs", "blob_refs", "definitions"} {
		var n int
		if err := db2.SQL().QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&n); err != nil || n != 1 {
			t.Fatalf("缺表 %s", tbl)
		}
	}
}

func TestPluginMigration(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	ms := []Migration{{Version: 1, Stmts: []string{`CREATE TABLE p_demo_x(tenant TEXT, k TEXT)`}}}
	if err := db.Migrate(ctx, "demo", ms); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := db.Migrate(ctx, "demo", ms); err != nil { // 重复执行无害
		t.Fatalf("重复 Migrate: %v", err)
	}
	ms = append(ms, Migration{Version: 2, Stmts: []string{`ALTER TABLE p_demo_x ADD COLUMN v TEXT`}})
	if err := db.Migrate(ctx, "demo", ms); err != nil {
		t.Fatalf("v2: %v", err)
	}
	if _, err := db.SQL().Exec(`INSERT INTO p_demo_x(tenant,k,v) VALUES('local','a','b')`); err != nil {
		t.Fatalf("v2 列不存在: %v", err)
	}
}
