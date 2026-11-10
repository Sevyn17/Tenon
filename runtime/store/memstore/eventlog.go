package memstore

// 组件事件日志（框架设计 04 4.6，T4b）。语义与 sqlite 的 eventlog.go 一致。

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Sevyn17/tenon/runtime/store"
)

// Append 追加一条组件事件（store.EventLog）。
func (s *Store) Append(ctx context.Context, e store.ComponentEvent) (int64, error) {
	if err := store.CheckEventName(e.Name); err != nil {
		return 0, err
	}
	if e.Source == "" {
		return 0, fmt.Errorf("store: 事件缺来源")
	}
	if len(e.Data) == 0 {
		e.Data = json.RawMessage(`{}`)
	}
	if err := s.lock(); err != nil {
		return 0, err
	}
	defer s.mu.Unlock()
	if e.At == 0 {
		e.At = s.now()
	}
	e.Seq = int64(len(s.compEvents)) + 1
	s.compEvents = append(s.compEvents, e)
	return e.Seq, nil
}

// Read 返回 seq > after 的事件（store.EventLog）。
func (s *Store) Read(ctx context.Context, after int64, limit int) ([]store.ComponentEvent, error) {
	if err := s.lock(); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	limit = store.EventReadLimit(limit)
	var out []store.ComponentEvent
	for _, e := range s.compEvents {
		if e.Seq <= after {
			continue
		}
		out = append(out, e)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// Cursor 读订阅者的读位点（store.EventLog）。没有记录返回 0。
func (s *Store) Cursor(ctx context.Context, name string) (int64, error) {
	if err := s.lock(); err != nil {
		return 0, err
	}
	defer s.mu.Unlock()
	return s.compCursors[name], nil
}

// SetCursor 记下读位点（store.EventLog）。小于已记下的值时拒绝。
func (s *Store) SetCursor(ctx context.Context, name string, seq int64) error {
	if err := s.lock(); err != nil {
		return err
	}
	defer s.mu.Unlock()
	if seq < s.compCursors[name] {
		return fmt.Errorf("store: 读位点 %s 已在 %d，不能退回 %d", name, s.compCursors[name], seq)
	}
	s.compCursors[name] = seq
	return nil
}

// *Store 提供组件事件日志（store.EventLog）。
var _ store.EventLog = (*Store)(nil)
