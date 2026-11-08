package memstore

import (
	"context"

	"github.com/Sevyn17/tenon/runtime/store"
)

// Tx（05：与 sqlite 同形）。

// Tx 执行 fn；fn 出错时把全部状态恢复到进入 Tx 之前（审查 P1-55：原先没有回滚）。
// 内存实现的写操作各自立即生效，事务期间别的 goroutine 能看到中间状态；Tx 之间用 txMu 串行，
// 失败回滚会把事务期间别的 goroutine 的写入一起撤销——内存实现只用于测试，不在意这一点。
func (s *Store) Tx(ctx context.Context, fn func(store.Store) error) error {
	s.txMu.Lock()
	defer s.txMu.Unlock()
	if err := s.lock(); err != nil {
		return err
	}
	sn := s.snap()
	s.mu.Unlock()
	if err := fn(txView{s}); err != nil {
		s.mu.Lock()
		s.restore(sn)
		s.mu.Unlock()
		return err
	}
	return nil
}

// txView 事务内交给 fn 的视图：嵌套 Tx 直接在外层事务里执行（与 sqlite 一致），不再取 txMu，免得同一 goroutine 自锁。
type txView struct{ *Store }

func (v txView) Tx(ctx context.Context, fn func(store.Store) error) error { return fn(v) }
