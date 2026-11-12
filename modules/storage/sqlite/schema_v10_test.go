package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
)

// v10 给已有会话补历史事件：游标沿用原 sync_seq / rewind_seq，升级前保存的客户端游标继续有效（06 10.2.1 第 5 条）。
func TestV10BackfillsEvents(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)", filepath.ToSlash(path)))
	if err != nil {
		t.Fatal(err)
	}
	s.SetMaxOpenConns(1)
	old := &DB{sql: s}
	if err := old.migrate(ctx, "core", coreMigrations[:9]); err != nil {
		t.Fatalf("v9 迁移: %v", err)
	}
	c := newCore()
	// 用 v9 的列写会话行（现在的 CreateSession 会写后来加的列）。
	sh, hb, err := splitHeader(c.Snapshot().Header)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := putShell(ctx, tx, sh); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO sessions(tenant, id, header, shell_hash, updated_at) VALUES('local','o',?,?,1)`, string(hb), sh.hash); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// 用 v9 的写法直接写条目与归档（没有事件表）：a=1, b=2，回退占 3，c=4。
	for i, txt := range []string{"a", "b"} {
		_ = c.Record(session.Content{session.NewTextPart(txt)}, nil)
		e := c.Snapshot().Entries[i]
		b, _ := json.Marshal(e)
		if _, err := s.Exec(`INSERT INTO session_entries(tenant, session_id, idx, id, turn, kind, body, at, sync_seq) VALUES('local','o',?,?,1,'user',?,?,?)`,
			i, e.ID, string(b), e.At, i+1); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Exec(`INSERT INTO session_entries_archive(tenant, session_id, idx, id, body, truncated_at, rewind_seq)
		SELECT tenant, session_id, idx, id, body, 5, 3 FROM session_entries WHERE idx = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Exec(`UPDATE session_entries SET body = body, sync_seq = 4 WHERE idx = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Exec(`UPDATE sessions SET log_len = 2, next_sync_seq = 5 WHERE id = 'o'`); err != nil {
		t.Fatal(err)
	}
	s.Close()

	db, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatalf("升级: %v", err)
	}
	defer db.Close()
	evs, wm, err := db.Replay(ctx, "o", 0)
	if err != nil {
		t.Fatal(err)
	}
	if wm != 4 || len(evs) != 3 {
		t.Fatalf("回放 %d 条 wm=%d，期望 3 条（两条条目 + 一次回退）", len(evs), wm)
	}
	for i, want := range []struct {
		seq  int64
		kind string
	}{{1, store.EventEntry}, {3, store.EventRewind}, {4, store.EventEntry}} {
		if evs[i].Seq != want.seq || evs[i].Kind != want.kind {
			t.Fatalf("第 %d 条：%d %s，期望 %d %s", i, evs[i].Seq, evs[i].Kind, want.seq, want.kind)
		}
	}
	// 升级前保存的游标 3：只拿到之后的条目。
	if evs, _, err := db.Replay(ctx, "o", 3); err != nil || len(evs) != 1 || evs[0].Seq != 4 {
		t.Fatalf("旧游标续订：%+v %v", evs, err)
	}
}
