package sqlite

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"tenon/runtime/session"
	"tenon/runtime/store"
)

func importCore() session.Header {
	c := session.New(&session.Config{Model: "m", Tools: []session.ToolSpec{{Name: "echo", Schema: json.RawMessage(`{"type":"object"}`)}},
		Policy: session.DefaultPolicy()})
	return c.Snapshot().Header
}

// Import：源库的会话（含子会话）并进来、没记工作区的记上给的工作区；本库已有同 id 的不导（标题不被覆盖）。
func TestImport(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	src, err := Open(ctx, filepath.Join(dir, "old.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []store.SessionMeta{{ID: "p1", Title: "旧的"}, {ID: "p1/c", ParentID: "p1", Origin: "subagent"}, {ID: "dup", Title: "源库的"}} {
		if err := src.CreateSession(ctx, m, importCore(), nil); err != nil {
			t.Fatal(err)
		}
	}
	src.Close()

	db, err := Open(ctx, filepath.Join(dir, "new.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.CreateSession(ctx, store.SessionMeta{ID: "dup", Title: "本库的", Workspace: "other"}, importCore(), nil); err != nil {
		t.Fatal(err)
	}
	res, err := db.Import(ctx, filepath.Join(dir, "old.db"), "proj")
	if err != nil {
		t.Fatal(err)
	}
	if res.Sessions != 2 || len(res.Skipped) != 1 || res.Skipped[0] != "dup" {
		t.Fatalf("结果：%+v", res)
	}
	ls, err := db.ListSessionsIn(ctx, "proj", 0, 50, false)
	if err != nil || len(ls) != 2 {
		t.Fatalf("proj 的会话：%+v %v", ls, err)
	}
	l, err := db.Load(ctx, "p1", nil)
	if err != nil || l.Meta.Title != "旧的" || l.Meta.Workspace != "proj" {
		t.Fatalf("p1：%+v %v", l, err)
	}
	if l, _ := db.Load(ctx, "dup", nil); l.Meta.Title != "本库的" || l.Meta.Workspace != "other" {
		t.Fatalf("撞 id 的会话不应被覆盖：%+v", l.Meta)
	}
	// 再并一次：全部撞 id，什么都不导。
	if res, err := db.Import(ctx, filepath.Join(dir, "old.db"), "proj"); err != nil || res.Sessions != 0 {
		t.Fatalf("重复并库：%+v %v", res, err)
	}
}
