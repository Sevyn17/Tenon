package apphost

// 框架不再自带存储插件（docs/02 - 开发管理/20）：产品在自己的 main 包里空导入一个。
// 本包的测试走真实装配，这里登记一个内存存储插件顶上，只在测试里生效。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"

	"github.com/Sevyn17/tenon/runtime/assemble"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/store"
	"github.com/Sevyn17/tenon/runtime/store/memstore"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

func init() {
	component.Register("memstore", func() component.Component { return &memStorePlugin{} })
}

type memStorePlugin struct{ h *memHandle }

func (p *memStorePlugin) Meta() component.Meta {
	return component.Meta{Name: "memstore", Default: true, Provides: []string{assemble.ServiceDB}}
}

func (p *memStorePlugin) Setup(cx *component.Registrar) error {
	p.h = &memHandle{blobs: &memBlobs{data: map[string][]byte{}}}
	return cx.Provide("db", assemble.DBHandle(p.h))
}

func (p *memStorePlugin) Start(sc *component.Scope) error {
	db := memstore.New()
	p.h.db = db
	sc.OnClose(func(context.Context) error { return db.Close() })
	return nil
}

type memHandle struct {
	db    store.Store
	blobs *memBlobs
}

func (h *memHandle) DB() store.Store {
	if h.db == nil {
		return nil
	}
	return h.db
}
func (h *memHandle) Blobs() toolhub.Artifacts { return h.blobs }

// memBlobs 内存工件存储。read_artifact 只在有工件存储时登记，快照测试依赖它在工具列表里。
type memBlobs struct {
	mu   sync.Mutex
	data map[string][]byte
}

func (b *memBlobs) Put(_ context.Context, _, _ string, data []byte) (string, error) {
	sum := sha256.Sum256(data)
	ref := hex.EncodeToString(sum[:])
	b.mu.Lock()
	b.data[ref] = append([]byte(nil), data...)
	b.mu.Unlock()
	return ref, nil
}

func (b *memBlobs) Get(hash string) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	d, ok := b.data[hash]
	if !ok {
		return nil, fmt.Errorf("memstore: 没有工件 %s", hash)
	}
	return append([]byte(nil), d...), nil
}
