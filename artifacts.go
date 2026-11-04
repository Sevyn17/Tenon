package ybase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// memArtifacts 进程内工件存储（外部存储不带 blobs 时用；内容按 sha256 寻址）。
//
// 回收规则与 sqlite 的 Blobs 一致（2026-11-04 审查 P2 资源：原先只增不删）：记下引用每个工件的会话与最后写入时间；
// GC 先丢掉已不存在的会话的引用，再删没有引用、且超过宽限期未被写入的工件。assemble.Wire 发现 GC 方法后
// 交给 Host.Maintain 定期调用。
type memArtifacts struct {
	mu sync.Mutex
	m  map[string]*memBlob
	// exists 会话是否还在（会话删除后它的引用失效）；nil = 引用永远有效。
	exists func(ctx context.Context, sessionID string) (bool, error)
	now    func() time.Time
}

type memBlob struct {
	data    []byte
	refs    map[string]bool // 引用它的会话
	touched time.Time
}

func newMemArtifacts(exists func(ctx context.Context, sessionID string) (bool, error)) *memArtifacts {
	return &memArtifacts{m: map[string]*memBlob{}, exists: exists, now: time.Now}
}

func (a *memArtifacts) Put(ctx context.Context, sessionID, mime string, data []byte) (string, error) {
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.m[hash]
	if b == nil {
		b = &memBlob{data: append([]byte(nil), data...), refs: map[string]bool{}}
		a.m[hash] = b
	}
	b.touched = a.now()
	if sessionID != "" {
		b.refs[sessionID] = true
	}
	return hash, nil
}

func (a *memArtifacts) Get(hash string) ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	b, ok := a.m[hash]
	if !ok {
		return nil, errors.New("ybase: 工件不存在")
	}
	return append([]byte(nil), b.data...), nil
}

// GC 回收没有任何引用、且超过宽限期未被写入的工件，返回回收数。宽限期防止刚写入、引用它的条目还没落库的工件被误删。
func (a *memArtifacts) GC(ctx context.Context, grace time.Duration) (int, error) {
	// 先在锁外查会话是否还在（exists 会访问存储）。
	a.mu.Lock()
	sessions := map[string]bool{}
	for _, b := range a.m {
		for s := range b.refs {
			sessions[s] = true
		}
	}
	a.mu.Unlock()
	gone := map[string]bool{}
	if a.exists != nil {
		for s := range sessions {
			ok, err := a.exists(ctx, s)
			if err != nil {
				return 0, err
			}
			if !ok {
				gone[s] = true
			}
		}
	}
	cut := a.now().Add(-grace)
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for h, b := range a.m {
		for s := range b.refs {
			if gone[s] {
				delete(b.refs, s)
			}
		}
		if len(b.refs) == 0 && b.touched.Before(cut) {
			delete(a.m, h)
			n++
		}
	}
	return n, nil
}
