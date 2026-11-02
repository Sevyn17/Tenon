package memstore

import (
	"context"
	"sync"
	"time"

	"ybase-kernel/core/store"
)

var _ store.TraceStore = (*Store)(nil)

// RecordTrace 追加一条 trace 事件。
func (s *Store) RecordTrace(ctx context.Context, ev store.TraceEvent) error {
	if ev.At == 0 {
		ev.At = time.Now().UnixMilli()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	s.traces = append(s.traces, ev)
	return nil
}

// TraceEvents 按发生顺序列出某个 trace 的全部事件。
func (s *Store) TraceEvents(ctx context.Context, trace string) ([]store.TraceEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	out := []store.TraceEvent{}
	for _, ev := range s.traces {
		if ev.Trace == trace {
			out = append(out, ev)
		}
	}
	return out, nil
}

var _ = sync.Mutex{}
