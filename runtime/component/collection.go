package component

// 集合登记（框架设计 04 4.7、05 5.6，T4a）。
//
// 组件在 Setup 里登记集合与迁移，框架在 Assembly.Start 之前打开：先跑迁移，再 Open。
// 句柄在打开之前不能用，用了返回 ErrCollectionClosed。

import (
	"context"
	"fmt"

	"github.com/Sevyn17/tenon/runtime/store"
)

// ErrCollectionClosed 集合句柄还没打开（Assembly.Start 之前），或启动失败没打开成。
var ErrCollectionClosed = fmt.Errorf("component: 集合尚未打开")

// CollectionMigration 一次集合迁移（框架设计 05 5.6）。
//
// From 必须从 0 连续到该集合 spec.Version - 1，跳号拒绝启动：没写迁移函数的版本会被跳过，
// 旧数据就会按新版本读。Run 拿到的句柄是迁移中的旧版本，只保证 Get / Put / Delete 可用，
// Query 按旧 Indexes 走。Run 为 nil 表示该版本没有数据要改，只推进版本号。
type CollectionMigration struct {
	Collection string
	From       int
	// After 那些集合先迁完，写 "<组件>.<集合>"。环与未知依赖拒绝启动。
	After []string
	// Run 在存储事务里执行，失败整批回滚并拒绝启动。
	Run func(ctx context.Context, c store.Collection) error
}

// collectionReg 一个组件登记的集合。
type collectionReg struct {
	plugin string
	name   string
	spec   store.CollectionSpec
	handle *collectionHandle
}

// collectionHandle 交给组件的句柄。打开之前所有操作返回 ErrCollectionClosed。
type collectionHandle struct {
	inner store.Collection
}

func (h *collectionHandle) opened() store.Collection {
	if h.inner == nil {
		return closedCollection{}
	}
	return h.inner
}

func (h *collectionHandle) Get(ctx context.Context, part, key string) (store.Doc, error) {
	return h.opened().Get(ctx, part, key)
}
func (h *collectionHandle) Put(ctx context.Context, part string, doc store.Doc, ifVersion int64) error {
	return h.opened().Put(ctx, part, doc, ifVersion)
}
func (h *collectionHandle) Delete(ctx context.Context, part, key string, ifVersion int64) error {
	return h.opened().Delete(ctx, part, key, ifVersion)
}
func (h *collectionHandle) Query(ctx context.Context, part, index string, eq any, page store.Page) ([]store.Doc, store.Page, error) {
	return h.opened().Query(ctx, part, index, eq, page)
}

// closedCollection 还没打开的句柄。
type closedCollection struct{}

func (closedCollection) Get(context.Context, string, string) (store.Doc, error) {
	return store.Doc{}, ErrCollectionClosed
}
func (closedCollection) Put(context.Context, string, store.Doc, int64) error {
	return ErrCollectionClosed
}
func (closedCollection) Delete(context.Context, string, string, int64) error {
	return ErrCollectionClosed
}
func (closedCollection) Query(context.Context, string, string, any, store.Page) ([]store.Doc, store.Page, error) {
	return nil, store.Page{}, ErrCollectionClosed
}

// Collection 登记一个集合并返回句柄（04 4.7）。
//
// 只登记、不碰存储。句柄在 Assembly.Start 之前由 OpenCollections 打开，之前调用返回 ErrCollectionClosed。
// 同组件同名重复登记返回错误。
func (cx *Registrar) Collection(name string, spec store.CollectionSpec) (store.Collection, error) {
	spec, err := spec.Normalize()
	if err != nil {
		return nil, fmt.Errorf("插件 %s：%w", cx.plugin, err)
	}
	for _, r := range cx.asm.collections {
		if r.plugin == cx.plugin && r.name == name {
			return nil, fmt.Errorf("插件 %s 重复登记集合 %s", cx.plugin, name)
		}
	}
	h := &collectionHandle{}
	cx.asm.collections = append(cx.asm.collections, collectionReg{plugin: cx.plugin, name: name, spec: spec, handle: h})
	cx.asm.points = append(cx.asm.points, Registration{Plugin: cx.plugin, Point: "collection", Name: name})
	return h, nil
}

// CollectionMigration 登记一次集合迁移（04 4.7、05 5.6）。集合必须已登记。
func (cx *Registrar) CollectionMigration(m CollectionMigration) error {
	var spec store.CollectionSpec
	found := false
	for _, r := range cx.asm.collections {
		if r.plugin == cx.plugin && r.name == m.Collection {
			spec, found = r.spec, true
		}
	}
	if !found {
		return fmt.Errorf("插件 %s：集合 %s 还没登记，不能登记迁移", cx.plugin, m.Collection)
	}
	if m.From < 0 || m.From >= spec.Version {
		return fmt.Errorf("插件 %s：集合 %s 的迁移 From %d 超出结构版本 %d", cx.plugin, m.Collection, m.From, spec.Version)
	}
	for _, have := range cx.asm.collMigrations {
		if have.plugin == cx.plugin && have.m.Collection == m.Collection && have.m.From == m.From {
			return fmt.Errorf("插件 %s：集合 %s 的迁移 From %d 重复登记", cx.plugin, m.Collection, m.From)
		}
	}
	cx.asm.collMigrations = append(cx.asm.collMigrations, collectionMigration{plugin: cx.plugin, m: m})
	return nil
}

// collectionMigration 带上登记它的组件。
type collectionMigration struct {
	plugin string
	m      CollectionMigration
}

// OpenCollections 打开全部已登记的集合（T4a 执行方案 3.5）。
//
// 放在 Assembly.Start 之前：组件的 Start 里就要能用句柄。顺序是先跑迁移、再 Open——
// 反过来会把新 spec 写进 collections 而数据还是旧形状。
//
// db 是已打开的存储。没实现 CollectionStore 且有组件登记了集合时，返回"存储不支持集合"并带上组件名。
// 没有登记时直接返回，存储不必实现该接口。
func (a *Assembly) OpenCollections(ctx context.Context, db store.Store) error {
	if len(a.collections) == 0 {
		return nil
	}
	cs, ok := db.(store.CollectionStore)
	if !ok {
		return fmt.Errorf("存储不支持集合（%s 登记了集合）", a.collections[0].plugin)
	}
	if err := a.migrateCollections(ctx, db, cs); err != nil {
		return err
	}
	for _, r := range a.collections {
		c, err := cs.Open(ctx, store.Tenant, r.plugin, r.name, r.spec)
		if err != nil {
			return fmt.Errorf("插件 %s 的集合 %s 打开失败：%w", r.plugin, r.name, err)
		}
		r.handle.inner = c
	}
	return nil
}

// migrateCollections 按 After 拓扑排序，把每个集合从已记录的版本迁到 spec.Version。
func (a *Assembly) migrateCollections(ctx context.Context, db store.Store, cs store.CollectionStore) error {
	order, err := a.migrationOrder()
	if err != nil {
		return err
	}
	byName := map[string]collectionReg{}
	for _, r := range a.collections {
		byName[r.plugin+"."+r.name] = r
	}
	for _, id := range order {
		r := byName[id]
		cur, err := cs.CollectionVersion(ctx, store.Tenant, r.plugin, r.name)
		if err != nil {
			return fmt.Errorf("插件 %s 的集合 %s 读迁移版本：%w", r.plugin, r.name, err)
		}
		if cur > r.spec.Version {
			return fmt.Errorf("插件 %s 的集合 %s 已迁移到版本 %d，高于登记的 %d（不能降级）", r.plugin, r.name, cur, r.spec.Version)
		}
		for v := cur; v < r.spec.Version; v++ {
			m, ok := a.migrationFrom(r.plugin, r.name, v)
			if !ok {
				return fmt.Errorf("插件 %s 的集合 %s 缺从版本 %d 升级的迁移", r.plugin, r.name, v)
			}
			if err := a.runMigration(ctx, db, cs, r, m, v); err != nil {
				return err
			}
		}
	}
	return nil
}

// migrationFrom 找某个版本的迁移登记。
func (a *Assembly) migrationFrom(plugin, name string, from int) (CollectionMigration, bool) {
	for _, h := range a.collMigrations {
		if h.plugin == plugin && h.m.Collection == name && h.m.From == from {
			return h.m, true
		}
	}
	return CollectionMigration{}, false
}

// runMigration 跑一次迁移：旧版本的句柄 + 同一个事务里推进版本号。
func (a *Assembly) runMigration(ctx context.Context, db store.Store, cs store.CollectionStore, r collectionReg, m CollectionMigration, from int) error {
	return db.Tx(ctx, func(tx store.Store) error {
		txcs, ok := tx.(store.CollectionStore)
		if !ok {
			return fmt.Errorf("插件 %s 的集合 %s：事务视图不支持集合", r.plugin, r.name)
		}
		// 迁移用 BindCollection 而不是 Open：迁移期间结构还没定，Open 会把过渡结构写进登记表，
		// 随后按新结构打开就被判成冲突。第一次迁移（from 0）没有旧结构，只保证 Get / Put / Delete，
		// Query 会因索引不符被拒——这个版本本来也没有数据。
		old := r.spec
		old.Version = from
		if from == 0 {
			old.Version = 1
			old.Indexes = nil
		}
		c, err := txcs.BindCollection(ctx, store.Tenant, r.plugin, r.name, old)
		if err != nil {
			return fmt.Errorf("插件 %s 的集合 %s 迁移 v%d：%w", r.plugin, r.name, from, err)
		}
		if m.Run != nil {
			if err := m.Run(ctx, c); err != nil {
				return fmt.Errorf("插件 %s 的集合 %s 迁移 v%d：%w", r.plugin, r.name, from, err)
			}
		}
		return txcs.SetCollectionVersion(ctx, store.Tenant, r.plugin, r.name, from+1)
	})
}

// migrationOrder 按 After 拓扑排序，返回 "<组件>.<集合>"。环与未知依赖返回错误。
func (a *Assembly) migrationOrder() ([]string, error) {
	known := map[string]bool{}
	for _, r := range a.collections {
		known[r.plugin+"."+r.name] = true
	}
	deps := map[string][]string{}
	for _, h := range a.collMigrations {
		id := h.plugin + "." + h.m.Collection
		for _, after := range h.m.After {
			if !known[after] {
				return nil, fmt.Errorf("插件 %s 的集合 %s 依赖未知集合 %s", h.plugin, h.m.Collection, after)
			}
			deps[id] = append(deps[id], after)
		}
	}
	var order []string
	state := map[string]int{} // 0 未访问，1 访问中，2 已完成
	var walk func(string) error
	walk = func(id string) error {
		switch state[id] {
		case 1:
			return fmt.Errorf("集合迁移有环：%s", id)
		case 2:
			return nil
		}
		state[id] = 1
		for _, d := range deps[id] {
			if err := walk(d); err != nil {
				return err
			}
		}
		state[id] = 2
		order = append(order, id)
		return nil
	}
	for _, r := range a.collections {
		if err := walk(r.plugin + "." + r.name); err != nil {
			return nil, err
		}
	}
	return order, nil
}
