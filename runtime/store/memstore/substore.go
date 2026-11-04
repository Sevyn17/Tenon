package memstore

import (
	"context"

	"ybase/runtime/store"
)

// 按子存储访问与 Tx（05：与 sqlite 同形）。

func (s *Store) Sessions() store.SessionStore       { return s }
func (s *Store) Queue() store.InboxStore            { return s }
func (s *Store) Results() store.ToolResultStore     { return s }
func (s *Store) Definitions() store.DefinitionStore { return s }
func (s *Store) Leases() store.LeaseStore           { return s }
func (s *Store) Sleeps() store.SleepStore           { return s }
func (s *Store) Pending() store.CorrStore           { return s }
func (s *Store) OutboxQueue() store.OutboxStore     { return s }
func (s *Store) Traces() store.TraceStore           { return s }

// Tx 在内存实现里只是把 fn 执行一遍（各写操作本身立即生效，没有回滚），
// 用于在两种实现上跑同一套一致性用例；需要真事务的调用方必须在 sqlite 上跑。
func (s *Store) Tx(ctx context.Context, fn func(store.Store) error) error {
	return fn(s)
}
