package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"ybase/runtime/store"
)

// C8：Tx 的 fn 里误用外层句柄不再卡死（写连接只有一个，事务占着它）：写在 TxConnWait 内返回错误；
// 读走只读连接池（P1-33），不受影响、照常返回；fn 里用传进来的视图照常工作。
func TestTxOuterHandleDoesNotDeadlock(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "s.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	done := make(chan error, 1)
	go func() {
		done <- db.Tx(ctx, func(tx store.Store) error {
			if _, err := tx.Exists(ctx, "x"); err != nil {
				return err
			}
			if _, err := db.Exists(ctx, "x"); err != nil { // 外层句柄的读：走只读池，照常返回
				return err
			}
			// 外层句柄的写：要写连接，事务占着它
			_, err := db.Admit(ctx, "x", store.InboxItem{ItemID: "i1", Kind: store.InboxInput, Input: `[]`})
			return err
		})
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrOuterInTx) && !errors.Is(err, context.Canceled) {
			t.Fatalf("Tx 里误用外层句柄应报 ErrOuterInTx，得 %v", err)
		}
	case <-time.After(TxConnWait + 3*time.Second):
		t.Fatal("Tx 里误用外层句柄卡死了")
	}
	// 事务结束后外层句柄正常可用。
	if _, err := db.Exists(ctx, "x"); err != nil {
		t.Fatalf("事务结束后外层句柄应可用: %v", err)
	}
	// 别的 goroutine 在事务期间用外层句柄：等事务结束后照常执行（不报错）。
	release := make(chan struct{})
	txDone := make(chan error, 1)
	go func() {
		txDone <- db.Tx(ctx, func(tx store.Store) error { <-release; return nil })
	}()
	time.Sleep(50 * time.Millisecond)
	other := make(chan error, 1)
	go func() { _, err := db.Exists(ctx, "y"); other <- err }()
	time.Sleep(50 * time.Millisecond)
	close(release)
	if err := <-txDone; err != nil {
		t.Fatal(err)
	}
	if err := <-other; err != nil {
		t.Fatalf("事务期间别的调用应在事务结束后照常执行: %v", err)
	}
}
