package memstore

// inbox 与工具记录（语义同 modules/storage/sqlite 的 inbox.go / tool_results.go / toolstore.go）。

import (
	"context"
	"sort"
	"time"

	"github.com/Sevyn17/tenon/runtime/store"
)

// —— inbox ——//

// Admit 准入一条输入。true = 新写入；false = 同 id 已存在（含已消费，D6）。
func (s *Store) Admit(ctx context.Context, sessionID string, it store.InboxItem) (bool, error) {
	if err := s.lock(); err != nil {
		return false, err
	}
	defer s.mu.Unlock()
	if it.Trust == "" {
		it.Trust = "trusted"
	}
	for _, r := range s.inbox[sessionID] {
		if r.it.ItemID == it.ItemID {
			return false, nil
		}
	}
	it.At = time.UnixMilli(s.now())
	s.inbox[sessionID] = append(s.inbox[sessionID], inboxRow{it: it})
	return true, nil
}

// Inbox 按准入顺序列出未消费项。
func (s *Store) Inbox(ctx context.Context, sessionID string) ([]store.InboxItem, error) {
	if err := s.lock(); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	var out []store.InboxItem
	for _, r := range s.inbox[sessionID] {
		if !r.consumed {
			out = append(out, r.it)
		}
	}
	return out, nil
}

// InInbox 报告某项是否仍待处理（未消费）。
func (s *Store) InInbox(ctx context.Context, sessionID, itemID string) (bool, error) {
	if err := s.lock(); err != nil {
		return false, err
	}
	defer s.mu.Unlock()
	for _, r := range s.inbox[sessionID] {
		if r.it.ItemID == itemID {
			return !r.consumed, nil
		}
	}
	return false, nil
}

// —— 工具记录 ——//

// msTime 与 sqlite 一致：截止时间按毫秒存，零值保持零值。
func msTime(t time.Time) time.Time {
	if t.IsZero() || t.UnixMilli() == 0 {
		return time.Time{}
	}
	return time.UnixMilli(t.UnixMilli())
}

// insertTool 插入一条记录；已存在返回 ErrStateMismatch（调用方持锁）。
func (s *Store) insertTool(r store.ToolRecord) error {
	m := s.tools[r.SessionID]
	if m == nil {
		m = map[string]*toolRow{}
		s.tools[r.SessionID] = m
	}
	if _, ok := m[r.CallID]; ok {
		return store.ErrStateMismatch
	}
	r.Deadline = msTime(r.Deadline)
	m[r.CallID] = &toolRow{r: r, created: s.now()}
	return nil
}

// tool 取记录（调用方持锁）。
func (s *Store) tool(sessionID, callID string) *toolRow {
	return s.tools[sessionID][callID]
}

// advance 条件推进：当前状态须为 from，否则 ErrStateMismatch（调用方持锁）。
func (s *Store) advance(sessionID, callID, from string, f func(*store.ToolRecord)) error {
	t := s.tool(sessionID, callID)
	if t == nil || t.r.Status != from {
		return store.ErrStateMismatch
	}
	f(&t.r)
	return nil
}

// PutToolStarted 有副作用的工具执行前写 started。已存在返回 ErrStateMismatch（不覆盖）。
func (s *Store) PutToolStarted(ctx context.Context, sessionID, callID, argsHash string) error {
	if err := s.lock(); err != nil {
		return err
	}
	defer s.mu.Unlock()
	return s.insertTool(store.ToolRecord{SessionID: sessionID, CallID: callID, Status: store.ToolStarted, ArgsHash: argsHash})
}

// PutToolPending 写挂起记录。
func (s *Store) PutToolPending(ctx context.Context, r store.ToolRecord) error {
	if err := s.lock(); err != nil {
		return err
	}
	defer s.mu.Unlock()
	// 与 sqlite 插入的列一致：decision / delivered / output 不随挂起写入。
	return s.insertTool(store.ToolRecord{SessionID: r.SessionID, CallID: r.CallID, Status: store.ToolPending,
		Kind: r.Kind, ArgsHash: r.ArgsHash, Deadline: r.Deadline, TokenHash: r.TokenHash})
}

// Deliver 挂起结果送达：pending → delivered。
func (s *Store) Deliver(ctx context.Context, sessionID, callID, delivered, decision string) error {
	if err := s.lock(); err != nil {
		return err
	}
	defer s.mu.Unlock()
	return s.advance(sessionID, callID, store.ToolPending, func(r *store.ToolRecord) {
		r.Status, r.Delivered, r.Decision = store.ToolDelivered, delivered, decision
	})
}

// FinishTool 写 done。from 为期望的当前状态；from = "" 直接插入 done（只读工具）。
func (s *Store) FinishTool(ctx context.Context, sessionID, callID, from, output string) error {
	if err := s.lock(); err != nil {
		return err
	}
	defer s.mu.Unlock()
	if from == "" {
		return s.insertTool(store.ToolRecord{SessionID: sessionID, CallID: callID, Status: store.ToolDone, Output: output})
	}
	return s.advance(sessionID, callID, from, func(r *store.ToolRecord) {
		r.Status, r.Output = store.ToolDone, output
	})
}

// GetTool 读一条记录；不存在返回 (nil, nil)。
func (s *Store) GetTool(ctx context.Context, sessionID, callID string) (*store.ToolRecord, error) {
	if err := s.lock(); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	t := s.tool(sessionID, callID)
	if t == nil {
		return nil, nil
	}
	r := t.r
	return &r, nil
}

// sortedTools 按创建顺序列出会话的工具记录（调用方持锁）。
func (s *Store) sortedTools(sessionID string) []*toolRow {
	out := make([]*toolRow, 0, len(s.tools[sessionID]))
	for _, t := range s.tools[sessionID] {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].created < out[j].created })
	return out
}

// PendingTools 列出会话的挂起记录（字段与 sqlite 一致：只含 id / 种类 / 哈希 / 截止）。
func (s *Store) PendingTools(ctx context.Context, sessionID string) ([]store.ToolRecord, error) {
	if err := s.lock(); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	var out []store.ToolRecord
	for _, t := range s.sortedTools(sessionID) {
		if t.r.Status == store.ToolPending {
			out = append(out, store.ToolRecord{SessionID: sessionID, CallID: t.r.CallID, Status: store.ToolPending,
				Kind: t.r.Kind, ArgsHash: t.r.ArgsHash, Deadline: t.r.Deadline})
		}
	}
	return out, nil
}

// DeleteFinishedTools 清理 done / started 记录（pending 不动）。
func (s *Store) DeleteFinishedTools(ctx context.Context, sessionID string) error {
	if err := s.lock(); err != nil {
		return err
	}
	defer s.mu.Unlock()
	for id, t := range s.tools[sessionID] {
		if t.r.Status == store.ToolDone || t.r.Status == store.ToolStarted {
			delete(s.tools[sessionID], id)
		}
	}
	return nil
}

// StartTool 执行前写 started：无记录则插入；已批准（delivered）且参数哈希一致则推进为 started。
func (s *Store) StartTool(ctx context.Context, sessionID, callID, argsHash string) error {
	if err := s.lock(); err != nil {
		return err
	}
	defer s.mu.Unlock()
	t := s.tool(sessionID, callID)
	if t == nil {
		return s.insertTool(store.ToolRecord{SessionID: sessionID, CallID: callID, Status: store.ToolStarted, ArgsHash: argsHash})
	}
	if t.r.Status != store.ToolDelivered || t.r.ArgsHash != argsHash {
		return store.ErrStateMismatch
	}
	t.r.Status = store.ToolStarted
	return nil
}

// PendConfirm 写等确认记录；已存在不报错，仍 pending 时刷新参数哈希与截止。
func (s *Store) PendConfirm(ctx context.Context, sessionID, callID, argsHash string, deadline time.Time) error {
	if err := s.lock(); err != nil {
		return err
	}
	defer s.mu.Unlock()
	if t := s.tool(sessionID, callID); t != nil {
		if t.r.Status == store.ToolPending {
			t.r.ArgsHash, t.r.Deadline = argsHash, msTime(deadline)
		}
		return nil
	}
	return s.insertTool(store.ToolRecord{SessionID: sessionID, CallID: callID, Status: store.ToolPending,
		Kind: "confirm", ArgsHash: argsHash, Deadline: deadline})
}
