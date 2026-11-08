package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/Sevyn17/tenon/runtime/store"
)

// SessionStats 汇总一个会话的用量与最近一次请求组成（span 计数与 memstore 共用 Stats.AddSpan，口径一致）。
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
		st.AddSpan(kind, status, attrs)
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
