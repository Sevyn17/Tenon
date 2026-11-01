package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"ybase-kernel/core/session"
)

// v2 带数据的库升级到 v3：旧列保留，新列取缺省值，会话照常可读（框架设计 09 9.2，迁移只加列）。
func TestUpgradeV2WithData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)", filepath.ToSlash(path)))
	if err != nil {
		t.Fatal(err)
	}
	old := &DB{sql: s}
	if err := old.migrate(ctx, "core", coreMigrations[:2]); err != nil {
		t.Fatalf("v2 迁移: %v", err)
	}
	hdr := newCore().Snapshot().Header
	hdr.Config = nil
	hb, _ := session.MarshalHeader(&hdr)
	if _, err := s.Exec(`INSERT INTO session_shells(hash, body, created_at) VALUES('h0','null',0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Exec(`INSERT INTO sessions(tenant, id, agent_id, agent_version, header, shell_hash, updated_at)
		VALUES('local','old','a',7,?,'h0',0)`, string(hb)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Exec(`INSERT INTO definitions(tenant, kind, id, version, body, created_at) VALUES('local','agent','a',1,'x',0)`); err != nil {
		t.Fatal(err)
	}
	s.Close()

	db, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatalf("升级: %v", err)
	}
	defer db.Close()
	var ver int
	var hash, creator, meta, use string
	var key sql.NullString
	if err := db.SQL().QueryRow(`SELECT agent_version, agent_hash, creator, session_key, meta, use_config FROM sessions WHERE id='old'`).
		Scan(&ver, &hash, &creator, &key, &meta, &use); err != nil {
		t.Fatal(err)
	}
	if ver != 7 || hash != "" || creator != "" || key.Valid || meta != "{}" || use != "{}" {
		t.Fatalf("升级后列值不对: %d %q %q %v %q %q", ver, hash, creator, key, meta, use)
	}
	l, err := db.Load(ctx, "old", nil)
	if err != nil {
		t.Fatalf("升级后读会话: %v", err)
	}
	if l.Meta.AgentID != "a" || l.Meta.SessionKey != "" {
		t.Fatalf("Meta: %+v", l.Meta)
	}
	if body, v, err := db.GetDefinition(ctx, "agent", "a", 0); err != nil || body != "x" || v != 1 {
		t.Fatalf("旧定义: %q %d %v", body, v, err)
	}
}

func TestSessionMetaRoundTrip(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	m := SessionMeta{ID: "s1", AgentID: "a", AgentHash: "abc", Creator: "local", SessionKey: "k1",
		Meta: json.RawMessage(`{"order":"42"}`), UseConfig: json.RawMessage(`{"permission":{"mode":"ask"}}`)}
	if err := db.CreateSession(ctx, m, newCore().Snapshot().Header, nil); err != nil {
		t.Fatal(err)
	}
	l, err := db.Load(ctx, "s1", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := l.Meta
	if got.AgentHash != "abc" || got.Creator != "local" || got.SessionKey != "k1" ||
		string(got.Meta) != `{"order":"42"}` || string(got.UseConfig) != `{"permission":{"mode":"ask"}}` {
		t.Fatalf("回读: %+v", got)
	}
	// 缺省值
	if err := db.CreateSession(ctx, SessionMeta{ID: "s2"}, newCore().Snapshot().Header, nil); err != nil {
		t.Fatal(err)
	}
	l2, _ := db.Load(ctx, "s2", nil)
	if l2.Meta.SessionKey != "" || string(l2.Meta.Meta) != "{}" || string(l2.Meta.UseConfig) != "{}" {
		t.Fatalf("缺省: %+v", l2.Meta)
	}
}

// 会话键唯一约束：（租户, 定义, 创建者, key）；不带 key 不受约束（B19 修订）。
func TestSessionKeyUnique(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	h := newCore().Snapshot().Header
	mk := func(id, agent, creator, key string) error {
		return db.CreateSession(ctx, SessionMeta{ID: id, AgentID: agent, Creator: creator, SessionKey: key}, h, nil)
	}
	if err := mk("a1", "客服", "u1", "k"); err != nil {
		t.Fatal(err)
	}
	if err := mk("a2", "客服", "u1", "k"); err == nil {
		t.Fatal("同（定义, 创建者, key）应冲突")
	}
	for i, c := range [][3]string{{"客服", "u2", "k"}, {"日报", "u1", "k"}, {"客服", "u1", "k2"}, {"客服", "u1", ""}, {"客服", "u1", ""}} {
		if err := mk(fmt.Sprintf("b%d", i), c[0], c[1], c[2]); err != nil {
			t.Fatalf("%v 不应冲突: %v", c, err)
		}
	}
}

// 定义写入时记整包内容哈希（D1）。
func TestDefinitionHash(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	if _, err := db.PutDefinition(ctx, "agent", "a", "one"); err != nil {
		t.Fatal(err)
	}
	var h string
	db.SQL().QueryRow(`SELECT hash FROM definitions WHERE id='a'`).Scan(&h)
	if len(h) != 64 {
		t.Fatalf("hash = %q", h)
	}
}
