package sqlite

// BlobStore 默认实现（data-contract §4.1）：文件本体在 <root>/<sha256>，元数据在 blobs 表，
// 会话引用在 blob_refs。先写文件、再写引用（"先上传工件、再写引用它的条目"）。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Blobs 工件存储。
type Blobs struct {
	db   *DB
	root string
}

// NewBlobs 以 root 为文件目录（通常是 .ybase/blobs）。
func (db *DB) NewBlobs(root string) (*Blobs, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &Blobs{db: db, root: root}, nil
}

// Put 写入内容（内容寻址，重复写无害），并登记会话引用；返回 sha256。
func (b *Blobs) Put(ctx context.Context, sessionID, mime string, data []byte) (string, error) {
	sum := sha256.Sum256(data)
	h := hex.EncodeToString(sum[:])
	p := b.path(h)
	if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
		tmp := p + ".tmp"
		if err := os.WriteFile(tmp, data, 0o644); err != nil {
			return "", err
		}
		if err := os.Rename(tmp, p); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	now := time.Now().UnixMilli()
	if _, err := b.db.q().ExecContext(ctx, `INSERT INTO blobs(sha256, size, mime, created_at, touched_at) VALUES(?,?,?,?,?)
		ON CONFLICT(sha256) DO UPDATE SET touched_at = excluded.touched_at`, h, len(data), mime, now, now); err != nil {
		return "", err
	}
	if sessionID != "" {
		if _, err := b.db.q().ExecContext(ctx, `INSERT INTO blob_refs(tenant, session_id, sha256) VALUES(?,?,?)
			ON CONFLICT DO NOTHING`, Tenant, sessionID, h); err != nil {
			return "", err
		}
	}
	return h, nil
}

// Get 读内容，并校验哈希（文件被外部改坏时报错，不返回错误内容）。
func (b *Blobs) Get(h string) ([]byte, error) {
	data, err := os.ReadFile(b.path(h))
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != h {
		return nil, fmt.Errorf("blob %s 内容校验失败", h)
	}
	return data, nil
}

// GC 回收没有任何引用、且超过宽限期未被触碰的文件（data-contract §6）。
func (b *Blobs) GC(ctx context.Context, grace time.Duration) (int, error) {
	cut := time.Now().Add(-grace).UnixMilli()
	rows, err := b.db.q().QueryContext(ctx, `SELECT sha256 FROM blobs WHERE touched_at < ?
		AND sha256 NOT IN (SELECT sha256 FROM blob_refs)`, cut)
	if err != nil {
		return 0, err
	}
	var dead []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			rows.Close()
			return 0, err
		}
		dead = append(dead, h)
	}
	rows.Close()
	for _, h := range dead {
		if err := os.Remove(b.path(h)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return 0, err
		}
		if _, err := b.db.q().ExecContext(ctx, `DELETE FROM blobs WHERE sha256 = ?`, h); err != nil {
			return 0, err
		}
	}
	return len(dead), nil
}

func (b *Blobs) path(h string) string { return filepath.Join(b.root, h) }
