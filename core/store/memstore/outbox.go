package memstore

import (
	"context"
	"sort"

	"ybase-kernel/core/store"
)

// Outbox 返回待外发的信封（08 9.6）。
func (s *Store) Outbox(ctx context.Context, limit int) ([]store.OutboxItem, error) {
	if err := s.lock(); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	out := append([]store.OutboxItem(nil), s.outbox...)
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
			s.outbox = append(s.outbox[:i], s.outbox[i+1:]...)
			return nil
		}
	}
	return store.ErrNotFound
}
