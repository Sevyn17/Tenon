package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"ybase/runtime/store"
)

// SessionStats 汇总一个会话的用量与最近一次请求组成。
func (db *DB) SessionStats(ctx context.Context, sessionID string) (store.Stats, error) {
	var st store.Stats
	rows, err := db.rq().QueryContext(ctx, `SELECT kind, status, attrs FROM trace_spans
		WHERE tenant = ? AND session_id = ? AND kind IN ('attempt','tool','compact')`, store.Tenant, sessionID)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind, status, attrs string
		if err := rows.Scan(&kind, &status, &attrs); err != nil {
			return st, err
		}
		switch kind {
		case "tool":
			st.ToolCalls++
			continue
		case "compact":
			if status == "ok" {
				st.Compacts++
			}
			continue
		}
		st.Attempts++
		if status == "error" {
			st.Errors++
		}
		var a struct {
			Input, Output, CacheRead, CacheWrite uint64
			Source                               string `json:"usage_source"`
		}
		if attrs != "" {
			var m map[string]json.RawMessage
			if json.Unmarshal([]byte(attrs), &m) == nil {
				num := func(k string) uint64 {
					var v uint64
					_ = json.Unmarshal(m[k], &v)
					return v
				}
				a.Input, a.Output, a.CacheRead, a.CacheWrite = num("input"), num("output"), num("cache_read"), num("cache_write")
				_ = json.Unmarshal(m["usage_source"], &a.Source)
			}
		}
		if a.Source != "Reported" {
			st.Missing++
			continue
		}
		st.Reported++
		st.Input += a.Input
		st.Output += a.Output
		st.CacheRead += a.CacheRead
		st.CacheWrite += a.CacheWrite
	}
	if err := rows.Err(); err != nil {
		return st, err
	}
	var comp string
	err = db.rq().QueryRowContext(ctx, `SELECT request_id, composition FROM session_requests
		WHERE tenant = ? AND session_id = ? ORDER BY created_at DESC, rowid DESC LIMIT 1`, store.Tenant, sessionID).Scan(&st.LastRequest, &comp)
	if err != nil && err != sql.ErrNoRows {
		return st, err
	}
	if comp != "" {
		st.Composition = json.RawMessage(comp)
	}
	return st, nil
}
