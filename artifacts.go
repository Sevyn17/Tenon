package ybase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
)

// memArtifacts 进程内工件存储（外部存储不带 blobs 时用；内容按 sha256 寻址）。
type memArtifacts struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (a *memArtifacts) Put(ctx context.Context, sessionID, mime string, data []byte) (string, error) {
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	a.mu.Lock()
	defer a.mu.Unlock()
	a.m[hash] = append([]byte(nil), data...)
	return hash, nil
}

func (a *memArtifacts) Get(hash string) ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	v, ok := a.m[hash]
	if !ok {
		return nil, errors.New("ybase: 工件不存在")
	}
	return append([]byte(nil), v...), nil
}
