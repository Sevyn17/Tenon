package sqlite

// 组件事件日志（框架设计 04 4.6，T4b）。表在核心迁移 v14。
//
// 序号全局一条、只追加。事务里追加的暂存在事务视图上，外层提交成功后才可见
// （与 Commit 的持久事件同一做法，见 substore.go）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Sevyn17/tenon/runtime/store"
)

// Append 追加一条组件事件（store.EventLog）。Seq 由存储分配。
func (db *DB) Append(ctx context.Context, e store.ComponentEvent) (int64, error) {
	if err := store.CheckEventName(e.Name); err != nil {
		return 0, err
	}
	if e.Source == "" {
		return 0, fmt.Errorf("store: 事件缺来源")
	}
	data := e.Data
	if len(data) == 0 {
		data = json.RawMessage(`{}`)
	}
	at := e.At
	if at == 0 {
		at = time.Now().UnixMilli()
	}
	var seq int64
	err := db.q().QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM component_events WHERE tenant = ?`,
		store.Tenant).Scan(&seq)
	if err != nil {
		return 0, err
	}
	if _, err := db.q().ExecContext(ctx, `INSERT INTO component_events(tenant, seq, at, source, name, data)
		VALUES(?, ?, ?, ?, ?, ?)`, store.Tenant, seq, at, e.Source, e.Name, string(data)); err != nil {
		return 0, err
	}
	return seq, nil
}

// Read 返回 seq > after 的事件（store.EventLog）。
func (db *DB) Read(ctx context.Context, after int64, limit int) ([]store.ComponentEvent, error) {
	rows, err := db.rq().QueryContext(ctx, `SELECT seq, at, source, name, data FROM component_events
		WHERE tenant = ? AND seq > ? ORDER BY seq LIMIT ?`, store.Tenant, after, store.EventReadLimit(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.ComponentEvent
	for rows.Next() {
		var e store.ComponentEvent
		var data string
		if err := rows.Scan(&e.Seq, &e.At, &e.Source, &e.Name, &data); err != nil {
			return nil, err
		}
		e.Data = json.RawMessage(data)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Cursor 读订阅者的读位点（store.EventLog）。没有记录返回 0。
func (db *DB) Cursor(ctx context.Context, name string) (int64, error) {
	var seq int64
	err := db.rq().QueryRowContext(ctx, `SELECT seq FROM component_cursors WHERE tenant = ? AND name = ?`,
		store.Tenant, name).Scan(&seq)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return seq, err
}

// SetCursor 记下读位点（store.EventLog）。小于已记下的值时拒绝。
func (db *DB) SetCursor(ctx context.Context, name string, seq int64) error {
	have, err := db.Cursor(ctx, name)
	if err != nil {
		return err
	}
	if seq < have {
		return fmt.Errorf("store: 读位点 %s 已在 %d，不能退回 %d", name, have, seq)
	}
	_, err = db.q().ExecContext(ctx, `INSERT INTO component_cursors(tenant, name, seq) VALUES(?, ?, ?)
		ON CONFLICT(tenant, name) DO UPDATE SET seq = excluded.seq`, store.Tenant, name, seq)
	return err
}

// *DB 提供组件事件日志（store.EventLog）。
var _ store.EventLog = (*DB)(nil)
