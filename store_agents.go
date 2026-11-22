package tenon

// 存储里的智能体定义（Mortise docs/63 T-3）：WithStoreAgents 把 definitions 表接成定义来源，
// 排在全部来源之前。租户层同名定义因此能遮蔽插件包与组件模板之外的平台定义（副本），
// 墓碑版本让名字解析回落到下一层（agentdef.IsTombstone）。

import (
	"context"
	"errors"

	"github.com/Sevyn17/tenon/runtime/agentdef"
	"github.com/Sevyn17/tenon/runtime/assemble"
	"github.com/Sevyn17/tenon/runtime/store"
)

// WithStoreAgents 把存储的 definitions 表（kind = "agent"）接成定义来源。
//
// 存储往往由组件提供，Start 之前还没打开：这之前来源按"空"处理（List 为空、Load 不存在），
// 所以装配期的定义校验看不到存储里的定义；它们在写入时（agentdef.StoreSource.Save）解析校验，
// 打开会话时按 [use.*] 再校验一次。存储没实现 store.DefinitionStore 时同样按空处理。
func WithStoreAgents() Option {
	return func(c *config) error {
		c.storeAgents = true
		return nil
	}
}

// handleDefs 按需从存储句柄取 DefinitionStore（句柄在 Start 后才有 DB）。
type handleDefs struct{ h assemble.DBHandle }

func (d handleDefs) db() (store.DefinitionStore, bool) {
	if d.h == nil {
		return nil, false
	}
	st := d.h.DB()
	if st == nil {
		return nil, false
	}
	ds, ok := st.(store.DefinitionStore)
	return ds, ok
}

func (d handleDefs) PutDefinition(ctx context.Context, kind, id, body string) (int, string, error) {
	ds, ok := d.db()
	if !ok {
		return 0, "", errors.New("tenon: 存储还没打开或不支持定义")
	}
	return ds.PutDefinition(ctx, kind, id, body)
}

func (d handleDefs) GetDefinition(ctx context.Context, kind, id string, version int) (string, int, error) {
	ds, ok := d.db()
	if !ok {
		return "", 0, store.ErrNotFound
	}
	return ds.GetDefinition(ctx, kind, id, version)
}

func (d handleDefs) ListDefinitions(ctx context.Context, kind string) ([]string, error) {
	ds, ok := d.db()
	if !ok {
		return nil, nil
	}
	return ds.ListDefinitions(ctx, kind)
}

// DefinitionByHash 透传存储的按哈希查询（store.DefinitionHashLookup）；不支持时按不存在处理。
func (d handleDefs) DefinitionByHash(ctx context.Context, kind, hash string) (string, string, int, error) {
	ds, ok := d.db()
	if !ok {
		return "", "", 0, store.ErrNotFound
	}
	hl, ok := ds.(store.DefinitionHashLookup)
	if !ok {
		return "", "", 0, store.ErrNotFound
	}
	return hl.DefinitionByHash(ctx, kind, hash)
}

// StoreAgents 存储定义来源（WithStoreAgents 时非 nil）：产品用它写副本、写墓碑、按哈希还原。
func (a *App) StoreAgents() *agentdef.StoreSource { return a.storeDefs }
