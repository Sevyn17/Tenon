package sqlite

// AgentStore 的定义类数据（data-contract §4.2 definitions）：每次修改插入新版本，不原地更新。

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"tenon/runtime/store"
	"time"
)

// PutDefinition 写入新版本，返回版本号（从 1 起）与内容哈希。
func (db *DB) PutDefinition(ctx context.Context, kind, id, body string) (int, string, error) {
	var v int
	sum := sha256.Sum256([]byte(body))
	hash := hex.EncodeToString(sum[:])
	err := db.inTx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM definitions WHERE tenant = ? AND kind = ? AND id = ?`,
			store.Tenant, kind, id).Scan(&v); err != nil {
			return err
		}
		v++
		_, err := tx.ExecContext(ctx, `INSERT INTO definitions(tenant, kind, id, version, body, hash, created_at) VALUES(?,?,?,?,?,?,?)`,
			store.Tenant, kind, id, v, body, hash, time.Now().UnixMilli())
		return err
	})
	return v, hash, err
}

// GetDefinition 读指定版本；version <= 0 取最新。不存在返回 ErrNotFound。
func (db *DB) GetDefinition(ctx context.Context, kind, id string, version int) (string, int, error) {
	q := `SELECT body, version FROM definitions WHERE tenant = ? AND kind = ? AND id = ? AND version = ?`
	args := []any{store.Tenant, kind, id, version}
	if version <= 0 {
		q = `SELECT body, version FROM definitions WHERE tenant = ? AND kind = ? AND id = ? ORDER BY version DESC LIMIT 1`
		args = args[:3]
	}
	var body string
	var v int
	err := db.rq().QueryRowContext(ctx, q, args...).Scan(&body, &v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, store.ErrNotFound
	}
	return body, v, err
}

// ListDefinitions 列出 kind 下出现过的定义 id（每个 id 一次，按 id 升序）。
func (db *DB) ListDefinitions(ctx context.Context, kind string) ([]string, error) {
	rows, err := db.rq().QueryContext(ctx,
		`SELECT DISTINCT id FROM definitions WHERE tenant = ? AND kind = ? ORDER BY id`, store.Tenant, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
