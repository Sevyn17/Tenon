package sqlite

// 导出（data-contract D1）：一个会话的完整事实按 JSONL 输出，一行一条记录。
// 记录种类：session / entry / archived / request / tool / span。用于备份、排查与轨迹投影。

import (
	"context"
	"encoding/json"
	"io"
)

// Export 把会话写成 JSONL。
func (db *DB) Export(ctx context.Context, sessionID string, w io.Writer) error {
	enc := json.NewEncoder(w)
	emit := func(kind string, v any) error { return enc.Encode(map[string]any{"kind": kind, "data": v}) }

	var meta struct {
		ID, AgentID, AgentHash, Creator, ParentID, Origin, Status, RunState, Title, Header, ShellHash string
		ForkUpto, LogLen                                                                              int
		UpdatedAt                                                                                     int64
	}
	if err := db.q().QueryRowContext(ctx, `SELECT id, agent_id, agent_hash, creator, parent_id, origin, status, run_state, title, header, shell_hash,
		fork_upto, log_len, updated_at FROM sessions WHERE tenant = ? AND id = ?`, Tenant, sessionID).
		Scan(&meta.ID, &meta.AgentID, &meta.AgentHash, &meta.Creator, &meta.ParentID, &meta.Origin, &meta.Status, &meta.RunState, &meta.Title,
			&meta.Header, &meta.ShellHash, &meta.ForkUpto, &meta.LogLen, &meta.UpdatedAt); err != nil {
		return ErrNotFound
	}
	shellBody, _ := db.ShellBody(ctx, meta.ShellHash)
	if err := emit("session", map[string]any{
		"id": meta.ID, "agent_id": meta.AgentID, "agent_hash": meta.AgentHash, "creator": meta.Creator,
		"parent_id": meta.ParentID, "origin": meta.Origin, "fork_upto": meta.ForkUpto,
		"status": meta.Status, "run_state": meta.RunState, "title": meta.Title, "log_len": meta.LogLen,
		"updated_at": meta.UpdatedAt, "header": json.RawMessage(meta.Header), "config": json.RawMessage(nonEmpty(shellBody)),
	}); err != nil {
		return err
	}

	q := func(kind, query string, scan func(*rowsScanner) (any, error)) error {
		rows, err := db.q().QueryContext(ctx, query, Tenant, sessionID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, err := scan(&rowsScanner{rows.Scan})
			if err != nil {
				return err
			}
			if err := emit(kind, v); err != nil {
				return err
			}
		}
		return rows.Err()
	}
	if err := q("entry", `SELECT idx, sync_seq, body FROM session_entries WHERE tenant = ? AND session_id = ? ORDER BY idx`,
		func(s *rowsScanner) (any, error) {
			var idx, seq int
			var body string
			err := s.scan(&idx, &seq, &body)
			return map[string]any{"idx": idx, "sync_seq": seq, "entry": json.RawMessage(body)}, err
		}); err != nil {
		return err
	}
	if err := q("archived", `SELECT idx, rewind_seq, truncated_at, body FROM session_entries_archive
		WHERE tenant = ? AND session_id = ? ORDER BY rewind_seq, idx`,
		func(s *rowsScanner) (any, error) {
			var idx, seq int
			var at int64
			var body string
			err := s.scan(&idx, &seq, &at, &body)
			return map[string]any{"idx": idx, "rewind_seq": seq, "truncated_at": at, "entry": json.RawMessage(body)}, err
		}); err != nil {
		return err
	}
	if err := q("request", `SELECT request_id, shell_hash, prefix_base, prefix_end, compaction_view, suffix, final_hash, composition, created_at
		FROM session_requests WHERE tenant = ? AND session_id = ? ORDER BY created_at`,
		func(s *rowsScanner) (any, error) {
			var id, sh, cv, sf, fh, comp string
			var pb, pe int
			var at int64
			err := s.scan(&id, &sh, &pb, &pe, &cv, &sf, &fh, &comp, &at)
			return map[string]any{"request_id": id, "shell_hash": sh, "prefix_base": pb, "prefix_end": pe,
				"compaction_view": rawOrNil(cv), "suffix": rawOrNil(sf), "final_hash": fh, "composition": rawOrNil(comp), "created_at": at}, err
		}); err != nil {
		return err
	}
	if err := q("tool", `SELECT call_id, status, kind, args_hash, decision, output, created_at
		FROM tool_results WHERE tenant = ? AND session_id = ? ORDER BY created_at`,
		func(s *rowsScanner) (any, error) {
			var id, st, k, ah, dec, out string
			var at int64
			err := s.scan(&id, &st, &k, &ah, &dec, &out, &at)
			return map[string]any{"call_id": id, "status": st, "kind": k, "args_hash": ah, "decision": dec,
				"output": rawOrNil(out), "created_at": at}, err
		}); err != nil {
		return err
	}
	return q("span", `SELECT span_id, parent_span, kind, name, started_at, ended_at, status, attrs
		FROM trace_spans WHERE tenant = ? AND session_id = ? ORDER BY started_at`,
		func(s *rowsScanner) (any, error) {
			var id, p, k, n, st, attrs string
			var a, b int64
			err := s.scan(&id, &p, &k, &n, &a, &b, &st, &attrs)
			return map[string]any{"span_id": id, "parent": p, "kind": k, "name": n, "started_at": a, "ended_at": b,
				"status": st, "attrs": rawOrNil(attrs)}, err
		})
}

type rowsScanner struct{ scan func(...any) error }

func rawOrNil(s string) any {
	if s == "" || !json.Valid([]byte(s)) {
		if s == "" {
			return nil
		}
		return s
	}
	return json.RawMessage(s)
}

func nonEmpty(b []byte) []byte {
	if len(b) == 0 {
		return []byte("null")
	}
	return b
}
