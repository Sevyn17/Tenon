package memstore

// 会话写租约（02 2.2 fencing）：与 sqlite 实现同语义。

import (
	"context"
	"fmt"

	"tenon/runtime/store"
)

// AcquireLease 抢租约并返回新纪元（从 1 起）。会话不存在返回 ErrNotFound。
func (s *Store) AcquireLease(ctx context.Context, sessionID, holder string) (int64, error) {
	if holder == "" {
		return 0, fmt.Errorf("%w: 租约持有者不能为空", store.ErrLeaseLost)
	}
	if err := s.lock(); err != nil {
		return 0, err
	}
	defer s.mu.Unlock()
	x := s.live(sessionID)
	if x == nil {
		return 0, store.ErrNotFound
	}
	x.leaseHolder = holder
	x.leaseEpoch++
	return x.leaseEpoch, nil
}

// RenewLease 续租：holder 与 epoch 都匹配才成功，否则 ErrLeaseLost。
func (s *Store) RenewLease(ctx context.Context, sessionID, holder string, epoch int64) error {
	if holder == "" || epoch <= 0 {
		return store.ErrLeaseLost
	}
	if err := s.lock(); err != nil {
		return err
	}
	defer s.mu.Unlock()
	x := s.live(sessionID)
	if x == nil || x.leaseHolder != holder || x.leaseEpoch != epoch {
		return fmt.Errorf("%w: 会话 %s 纪元 %d 不再由 %s 持有", store.ErrLeaseLost, sessionID, epoch, holder)
	}
	x.leaseHolder = holder
	return nil
}

// ReleaseLease 释放租约：holder/epoch 匹配才清空持有者；不匹配忽略（幂等释放）。
func (s *Store) ReleaseLease(ctx context.Context, sessionID, holder string, epoch int64) error {
	if holder == "" || epoch <= 0 {
		return nil
	}
	if err := s.lock(); err != nil {
		return err
	}
	defer s.mu.Unlock()
	x := s.live(sessionID)
	if x == nil || x.leaseHolder != holder || x.leaseEpoch != epoch {
		return nil
	}
	x.leaseHolder = ""
	return nil
}
