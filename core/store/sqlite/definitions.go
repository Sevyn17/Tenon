package sqlite

// AgentStore 的定义类数据（data-contract §4.2 definitions）：每次修改插入新版本，不原地更新。

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

// PutDefinition 写入新版本，返回版本号（从 1 起）。
func (db *DB) PutDefinition(ctx context.Context, kind, id, body string) (int, error) {
	var v int
	err := db.tx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM definitions WHERE tenant = ? AND kind = ? AND id = ?`,
			Tenant, kind, id).Scan(&v); err != nil {
			return err
		}
		v++
		sum := sha256.Sum256([]byte(body))
		_, err := tx.ExecContext(ctx, `INSERT INTO definitions(tenant, kind, id, version, body, hash, created_at) VALUES(?,?,?,?,?,?,?)`,
			Tenant, kind, id, v, body, hex.EncodeToString(sum[:]), time.Now().UnixMilli())
		return err
	})
	return v, err
}

// GetDefinition 读指定版本；version <= 0 取最新。不存在返回 ErrNotFound。
func (db *DB) GetDefinition(ctx context.Context, kind, id string, version int) (string, int, error) {
	q := `SELECT body, version FROM definitions WHERE tenant = ? AND kind = ? AND id = ? AND version = ?`
	args := []any{Tenant, kind, id, version}
	if version <= 0 {
		q = `SELECT body, version FROM definitions WHERE tenant = ? AND kind = ? AND id = ? ORDER BY version DESC LIMIT 1`
		args = args[:3]
	}
	var body string
	var v int
	err := db.sql.QueryRowContext(ctx, q, args...).Scan(&body, &v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, ErrNotFound
	}
	return body, v, err
}
