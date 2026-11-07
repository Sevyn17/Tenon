package memstore

import (
	"context"
	"sort"
	"time"

	"tenon/runtime/store"
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
	sort.SliceStable(out, func(i, j int) bool { return out[i].At < out[j].At }) // 与 sqlite ORDER BY at, rowid 一致
	return out, nil
}

// Prune 过期数据清理（见 store.PruneStore）。
func (s *Store) Prune(ctx context.Context, before time.Time) (int, error) {
	if err := s.lock(); err != nil {
		return 0, err
	}
	defer s.mu.Unlock()
	cut := before.UnixMilli()
	n := 0
	for corr, p := range s.pending {
		if (p.Used || (p.Deadline > 0 && p.Deadline < cut)) && p.CreatedAt < cut {
			delete(s.pending, corr)
			n++
		}
	}
	ko := s.outbox[:0]
	for _, o := range s.outbox {
		if (o.sent || o.dead) && o.CreatedAt < cut {
			n++
			continue
		}
		ko = append(ko, o)
	}
	s.outbox = ko
	kt := s.traces[:0]
	for _, ev := range s.traces {
		if ev.At < cut {
			n++
			continue
		}
		kt = append(kt, ev)
	}
	s.traces = kt
	// 会话事件按保留期清理（06 10.12 Q3），清掉的最大游标记进 eventFloor（与 sqlite 的 event_floor 一致）。
	for _, x := range s.sessions {
		ke := x.events[:0]
		for _, ev := range x.events {
			if ev.at < cut {
				x.eventFloor = max(x.eventFloor, ev.cursor)
				n++
				continue
			}
			ke = append(ke, ev)
		}
		x.events = ke
	}
	return n, nil
}
