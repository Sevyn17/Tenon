package tenon

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Sevyn17/tenon/runtime/store"
	"github.com/Sevyn17/tenon/runtime/store/sqlite"
)

// SQLite 存储自带 SQL 出口：SDK 装配不该因为"没有 store.SQL"把要建表的组件（workgroup）悄悄去掉
// （2026-11-04 审查 P2：原先 *sqlite.DB 不实现 store.SQL，出口只在 SQLOut 包装里）。
func TestSQLiteStoreKeepsWorkgroup(t *testing.T) {
	db, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "s.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := any(db).(store.SQL); !ok {
		t.Fatal("*sqlite.DB 应实现 store.SQL")
	}
	a, err := New(Default(), WithStore(db), WithWorkdir(t.TempDir()), WithModels(testHub(), "m"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(context.Background())
	found := false
	for _, n := range a.asm.Order() {
		if n == "workgroup" {
			found = true
		}
	}
	if !found {
		t.Fatalf("sqlite 存储下应装上 workgroup：%v", a.asm.Order())
	}
}
