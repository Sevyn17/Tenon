package memstore

import (
	"context"
	"fmt"
	"time"

	"ybase-kernel/core/store"
)

// IssueCorr 登记一条待回复（11 11.2 待回复表 / 13 13.3 D3）。
func (s *Store) IssueCorr(ctx context.Context, p store.Pending) error {
	if p.Corr == "" || p.SessionID == "" {
		return fmt.Errorf("memstore: corr 登记缺 corr 或 session_id")
	}
	if err := s.lock(); err != nil {
		return err
	}
	defer s.mu.Unlock()
	if _, ok := s.pending[p.Corr]; ok {
		return fmt.Errorf("memstore: corr %s 已登记", p.Corr)
	}
	s.pending[p.Corr] = p
	return nil
}

// ClaimCorr 核对并占用一条待回复：存在、未过期、未用过、发送者 = 预期回复方。
//
// 锁内改 used，保证并发的两封回信只有一个能成功（只收第一条）。
func (s *Store) ClaimCorr(ctx context.Context, corr, sessionID, from string) (store.Pending, error) {
	if err := s.lock(); err != nil {
		return store.Pending{}, err
	}
	defer s.mu.Unlock()
	p, ok := s.pending[corr]
	if !ok || p.Used || p.SessionID != sessionID {
		return store.Pending{}, store.ErrCorrRejected
	}
	if p.Deadline != 0 && p.Deadline <= time.Now().UnixMilli() {
		return store.Pending{}, store.ErrCorrRejected
	}
	if p.ReplyTo != "" && p.ReplyTo != from {
		return store.Pending{}, store.ErrCorrRejected
	}
	p.Used = true
	s.pending[corr] = p
	return p, nil
}

// PendingCorr 读一条登记（不存在返回 ErrNotFound）。
func (s *Store) PendingCorr(ctx context.Context, corr string) (store.Pending, error) {
	if err := s.lock(); err != nil {
		return store.Pending{}, err
	}
	defer s.mu.Unlock()
	p, ok := s.pending[corr]
	if !ok {
		return store.Pending{}, store.ErrNotFound
	}
	return p, nil
}

// VoidCorrAfter 作废某会话在日志位置 idx 及之后签发的未用 corr（Rewind 语义）。
// VoidCorr 作废一条 corr；已用过或不存在时什么也不做。
func (s *Store) VoidCorr(ctx context.Context, corr string) error {
	if err := s.lock(); err != nil {
		return err
	}
	defer s.mu.Unlock()
	if p, ok := s.pending[corr]; ok && !p.Used {
		p.Used = true
		s.pending[corr] = p
	}
	return nil
}

func (s *Store) VoidCorrAfter(ctx context.Context, sessionID string, idx int) (int, error) {
	if err := s.lock(); err != nil {
		return 0, err
	}
	defer s.mu.Unlock()
	n := 0
	for corr, p := range s.pending {
		if p.SessionID != sessionID || p.Used || p.IssuedIdx < idx {
			continue
		}
		p.Used = true
		s.pending[corr] = p
		n++
	}
	return n, nil
}
