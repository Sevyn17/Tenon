package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"ybase/runtime/session"
	"ybase/runtime/store"
)

// 写事务进行中，别的 goroutine 的读不排队、不报错（2026-11-04 审查 P1-33：原先单连接读写串行，
// Tx 超过 TxConnWait 时并发的合法读报 ErrOuterInTx / context.Canceled）。
func TestReadsDoNotWaitForTx(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "s.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	c := session.New(&session.Config{Model: "m", Policy: session.DefaultPolicy()})
	if err := db.CreateSession(ctx, store.SessionMeta{ID: "s1"}, c.Snapshot().Header, nil); err != nil {
		t.Fatal(err)
	}
	inTx := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- db.Tx(ctx, func(store.Store) error {
			close(inTx)
			<-release
			return nil
		})
	}()
	<-inTx
	start := time.Now()
	st, err := db.SessionStatus(ctx, "s1")
	took := time.Since(start)
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err != nil || st != store.StatusActive {
		t.Fatalf("事务进行中的并发读应成功：%q %v", st, err)
	}
	if took > time.Second {
		t.Fatalf("并发读等了 %v，应不必等写事务", took)
	}
}
