package sqlite

// 事件回放与会话列表（应用接口层，docs/10 - 框架模块/12 - 应用接口层.md §三、§四）。

import (
	"context"
	"encoding/json"
	"sort"

	"ybase-kernel/core/bus"
)

// OnCommit 设置提交成功后的持久事件回调（应用把它接到 bus.Publish，供 Follow 的实时部分）。
// 回调在提交的 goroutine 里同步执行，须很快返回。
func (db *DB) OnCommit(f func(bus.Envelope)) {
	db.mu.Lock()
	db.onCommit = f
	db.mu.Unlock()
}

func (db *DB) notify(evs []bus.Envelope) {
	db.mu.Lock()
	f := db.onCommit
	db.mu.Unlock()
	if f == nil {
		return
	}
	for _, e := range evs {
		f(e)
	}
}

// Replay 实现 bus.Source：返回 seq > after 的条目追加与回退事件（按 seq 升序）及水位。
func (db *DB) Replay(ctx context.Context, session string, after int64) ([]bus.Envelope, int64, error) {
	var next int64
	if err := db.sql.QueryRowContext(ctx, `SELECT next_sync_seq FROM sessions WHERE tenant = ? AND id = ?`, Tenant, session).Scan(&next); err != nil {
		return nil, 0, err
	}
	var out []bus.Envelope
	rows, err := db.sql.QueryContext(ctx, `SELECT idx, sync_seq, at, body FROM session_entries
		WHERE tenant = ? AND session_id = ? AND sync_seq > ? ORDER BY sync_seq`, Tenant, session, after)
	if err != nil {
		return nil, 0, err
	}
	for rows.Next() {
		var idx int
		var seq, at int64
		var body string
		if err := rows.Scan(&idx, &seq, &at, &body); err != nil {
			rows.Close()
			return nil, 0, err
		}
		out = append(out, bus.Envelope{V: bus.Version, Seq: seq, At: at, Session: session, Kind: EventEntry,
			Data: map[string]any{"idx": idx, "entry": json.RawMessage(body)}})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	rr, err := db.sql.QueryContext(ctx, `SELECT rewind_seq, MIN(idx), MAX(truncated_at) FROM session_entries_archive
		WHERE tenant = ? AND session_id = ? AND rewind_seq > ? GROUP BY rewind_seq`, Tenant, session, after)
	if err != nil {
		return nil, 0, err
	}
	for rr.Next() {
		var seq, at int64
		var to int
		if err := rr.Scan(&seq, &to, &at); err != nil {
			rr.Close()
			return nil, 0, err
		}
		out = append(out, bus.Envelope{V: bus.Version, Seq: seq, At: at, Session: session, Kind: EventRewind,
			Data: map[string]any{"to": to}})
	}
	rr.Close()
	if err := rr.Err(); err != nil {
		return nil, 0, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, next - 1, nil
}

// ListSessions 按更新时间倒序列出未删除会话；before > 0 时只列 updated_at < before 的（分页游标）。
// topOnly 只列顶层会话（不含子智能体 / 成员）。
func (db *DB) ListSessions(ctx context.Context, before int64, limit int, topOnly bool) ([]SessionInfo, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	q := `SELECT id, title, preview, run_state, origin, parent_id, updated_at FROM sessions
		WHERE tenant = ? AND status != 'deleted'`
	args := []any{Tenant}
	if before > 0 {
		q += ` AND updated_at < ?`
		args = append(args, before)
	}
	if topOnly {
		q += ` AND parent_id = ''`
	}
	q += ` ORDER BY updated_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := db.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionInfo
	for rows.Next() {
		var s SessionInfo
		if err := rows.Scan(&s.ID, &s.Title, &s.Preview, &s.RunState, &s.Origin, &s.ParentID, &s.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
