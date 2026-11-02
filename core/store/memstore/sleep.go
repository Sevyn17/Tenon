package memstore

// 会话休眠状态（02 2.2）：与 sqlite 实现同语义。

import (
	"context"
	"fmt"
	"sort"

	"ybase-kernel/core/store"
)

// SessionStatus 读会话生命周期状态（active / sleeping / deleted）。
func (s *Store) SessionStatus(ctx context.Context, sessionID string) (string, error) {
	if err := s.lock(); err != nil {
		return "", err
	}
	defer s.mu.Unlock()
	x := s.live(sessionID)
	if x == nil {
		return "", store.ErrNotFound
	}
	return x.status, nil
}

// SetSessionStatus 条件迁移：当前状态必须等于 from 才改为 to；epoch 非零时还要求持有该纪元的租约。
func (s *Store) SetSessionStatus(ctx context.Context, sessionID, from, to string, epoch int64) error {
	if err := s.lock(); err != nil {
		return err
	}
	defer s.mu.Unlock()
	x := s.live(sessionID)
	if x == nil {
		return store.ErrNotFound
	}
	if x.status != from {
		return fmt.Errorf("%w: 会话 %s 不是 %s 状态", store.ErrStateMismatch, sessionID, from)
	}
	if epoch != 0 && x.leaseEpoch != epoch {
		return fmt.Errorf("%w: 会话 %s 纪元 %d 不再有效", store.ErrStateMismatch, sessionID, epoch)
	}
	x.status = to
	x.updatedAt = s.now()
	return nil
}

// SessionsWithStatus 列出某状态的会话 id（按 id 升序，limit <= 0 表示不限）。
func (s *Store) SessionsWithStatus(ctx context.Context, status string, limit int) ([]string, error) {
	if err := s.lock(); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	out := []string{}
	for id, x := range s.sessions {
		if x.status == status {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
