package memstore

import (
	"context"
	"sort"
	"time"

	"tenon/runtime/store"
)

// ClaimOutbox 原子认领待派发信封（见 store.OutboxStore）。
func (s *Store) ClaimOutbox(ctx context.Context, holder string, lease time.Duration, limit int) ([]store.OutboxItem, error) {
	if err := s.lock(); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	now := time.Now().UnixMilli()
	idx := make([]int, 0, len(s.outbox))
	for i := range s.outbox {
		o := s.outbox[i]
		if o.sent || o.dead || (o.claimedBy != "" && o.claimUntil > now) {
			continue
		}
		idx = append(idx, i)
	}
	sort.Slice(idx, func(a, b int) bool {
		x, y := s.outbox[idx[a]], s.outbox[idx[b]]
		if x.CreatedAt != y.CreatedAt {
			return x.CreatedAt < y.CreatedAt
		}
		return x.ID < y.ID
	})
	if limit > 0 && len(idx) > limit {
		idx = idx[:limit]
	}
	out := make([]store.OutboxItem, 0, len(idx))
	for _, i := range idx {
		o := &s.outbox[i]
		o.claimedBy, o.claimUntil = holder, now+lease.Milliseconds()
		o.Attempts++
		out = append(out, o.OutboxItem)
	}
	return out, nil
}

// ReleaseOutbox 交还一条认领。
func (s *Store) ReleaseOutbox(ctx context.Context, id, holder string) error {
	if err := s.lock(); err != nil {
		return err
	}
	defer s.mu.Unlock()
	for i := range s.outbox {
		if s.outbox[i].ID == id && s.outbox[i].claimedBy == holder {
			s.outbox[i].claimedBy, s.outbox[i].claimUntil = "", 0
		}
	}
	return nil
}

// DeadOutbox 把一条信封标为死信。
func (s *Store) DeadOutbox(ctx context.Context, id, reason string) error {
	if err := s.lock(); err != nil {
		return err
	}
	defer s.mu.Unlock()
	for i := range s.outbox {
		if s.outbox[i].ID == id {
			s.outbox[i].dead, s.outbox[i].deadReason = true, reason
		}
	}
	return nil
}

// Outbox 返回待外发的信封（08 9.6）。
func (s *Store) Outbox(ctx context.Context, limit int) ([]store.OutboxItem, error) {
	if err := s.lock(); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	var out []store.OutboxItem
	for _, o := range s.outbox {
		if !o.sent && !o.dead {
			out = append(out, o.OutboxItem)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt < out[j].CreatedAt
		}
		return out[i].ID < out[j].ID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// MarkOutbox 标记一条外发信封已派发。
func (s *Store) MarkOutbox(ctx context.Context, id string) error {
	if err := s.lock(); err != nil {
		return err
	}
	defer s.mu.Unlock()
	for i := range s.outbox {
		if s.outbox[i].ID == id {
			s.outbox[i].sent = true // 保留行：同 id 重提交不会再入队（与 sqlite 一致）
			s.outbox[i].claimedBy, s.outbox[i].claimUntil = "", 0
			return nil
		}
	}
	return nil // 与 sqlite 一致：未知 id 不报错
}
