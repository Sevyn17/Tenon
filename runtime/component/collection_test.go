package component

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Sevyn17/tenon/runtime/ext"
	"github.com/Sevyn17/tenon/runtime/store"
	"github.com/Sevyn17/tenon/runtime/store/memstore"
)

// 集合登记（T4a 第 2 步）。只走登记入口，不依赖真实组件。

var collSeq atomic.Int64

// collPlugin 登记一个名字合法的测试组件（集合的组件名只允许小写、数字、下划线，最长 32）。
func collPlugin(t *testing.T, f *fake) string {
	t.Helper()
	name := fmt.Sprintf("c%d", collSeq.Add(1))
	f.meta.Name = name
	Register(name, func() Component { return f })
	return name
}

func specV(version int, indexes ...string) store.CollectionSpec {
	return store.CollectionSpec{Version: version, Indexes: indexes, Partition: "team"}
}

func TestCollectionNotOpenedBeforeStart(t *testing.T) {
	var handle store.Collection
	p := collPlugin(t, &fake{meta: Meta{}, setup: func(cx *Registrar) error {
		c, err := cx.Collection("items", specV(1, "status"))
		handle = c
		return err
	}})
	if _, err := Assemble(Spec{Plugins: entries(p)}); err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Get(context.Background(), "L", "k"); !errors.Is(err, ErrCollectionClosed) {
		t.Fatalf("Start 之前应返回集合尚未打开：%v", err)
	}
}

func TestCollectionOpensOnStart(t *testing.T) {
	db := memstore.New()
	var handle store.Collection
	var seenInStart bool
	p := collPlugin(t, &fake{meta: Meta{},
		setup: func(cx *Registrar) error {
			c, err := cx.Collection("items", specV(1, "status"))
			handle = c
			if err != nil {
				return err
			}
			return cx.CollectionMigration(CollectionMigration{Collection: "items", From: 0})
		},
		start: func(*Scope) error {
			_, err := handle.Get(context.Background(), "L", "k")
			seenInStart = errors.Is(err, store.ErrNotFound)
			return nil
		}})
	a, err := Assemble(Spec{Plugins: entries(p), Inject: map[string]any{ext.ServiceDB: dbHandle{db}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Start(NewRoot("t")); err != nil {
		t.Fatal(err)
	}
	if !seenInStart {
		t.Fatal("Start 里句柄应已打开")
	}
}

// dbHandle 测试用的存储句柄，只实现 Assembly.Start 要的 DB()。
type dbHandle struct{ store.Store }

func (h dbHandle) DB() store.Store { return h.Store }

func TestCollectionMigrationOrderAndRollback(t *testing.T) {
	db := memstore.New()
	ctx := context.Background()
	ran := []string{}
	boom := errors.New("迁移失败")

	owner := collPlugin(t, &fake{meta: Meta{}, setup: func(cx *Registrar) error {
		if _, err := cx.Collection("base", specV(1)); err != nil {
			return err
		}
		return cx.CollectionMigration(CollectionMigration{Collection: "base", From: 0,
			Run: func(context.Context, store.Collection) error { ran = append(ran, "base"); return nil }})
	}})
	user := collPlugin(t, &fake{meta: Meta{Requires: []string{ext.ServiceDB}}, setup: func(cx *Registrar) error {
		if _, err := cx.Collection("items", specV(2, "status")); err != nil {
			return err
		}
		if err := cx.CollectionMigration(CollectionMigration{Collection: "items", From: 0, After: []string{owner + ".base"},
			Run: func(ctx context.Context, c store.Collection) error {
				ran = append(ran, "items0")
				return c.Put(ctx, "L", store.Doc{Key: "k", Body: map[string]any{"team": "L"}}, 0)
			}}); err != nil {
			return err
		}
		return cx.CollectionMigration(CollectionMigration{Collection: "items", From: 1,
			Run: func(context.Context, store.Collection) error { ran = append(ran, "items1"); return boom }})
	}})
	a, err := Assemble(Spec{Plugins: entries(owner, user), Inject: map[string]any{ext.ServiceDB: dbHandle{db}}})
	if err != nil {
		t.Fatal(err)
	}
	err = a.Start(NewRoot("t"))
	if !errors.Is(err, boom) {
		t.Fatalf("迁移失败应拒绝启动：%v", err)
	}
	if strings.Join(ran, ",") != "base,items0,items1" {
		t.Fatalf("迁移顺序：%v", ran)
	}
	// 失败的那一步回滚：版本停在 1，前一步写入的文档还在。
	// 那一步用的是过渡结构（没有索引），按登记的新结构读会被判成结构不符，所以走 BindCollection。
	v, err := db.CollectionVersion(ctx, store.Tenant, user, "items")
	if err != nil || v != 1 {
		t.Fatalf("失败后版本应停在 1：%d %v", v, err)
	}
	c, err := db.BindCollection(ctx, store.Tenant, user, "items", specV(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(ctx, "L", "k"); err != nil {
		t.Fatalf("前一步提交的数据应保留：%v", err)
	}
}

func TestCollectionMigrationGapRejected(t *testing.T) {
	db := memstore.New()
	p := collPlugin(t, &fake{meta: Meta{}, setup: func(cx *Registrar) error {
		if _, err := cx.Collection("items", specV(2)); err != nil {
			return err
		}
		return cx.CollectionMigration(CollectionMigration{Collection: "items", From: 1})
	}})
	a, err := Assemble(Spec{Plugins: entries(p), Inject: map[string]any{ext.ServiceDB: dbHandle{db}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Start(NewRoot("t")); err == nil || !strings.Contains(err.Error(), "缺从版本 0") {
		t.Fatalf("跳号应拒绝启动：%v", err)
	}
}

func TestCollectionDuplicateRejected(t *testing.T) {
	p := collPlugin(t, &fake{meta: Meta{}, setup: func(cx *Registrar) error {
		if _, err := cx.Collection("items", specV(1)); err != nil {
			return err
		}
		_, err := cx.Collection("items", specV(1))
		return err
	}})
	if _, err := Assemble(Spec{Plugins: entries(p)}); err == nil || !strings.Contains(err.Error(), "重复登记") {
		t.Fatalf("同名重复登记应报错：%v", err)
	}
}

func TestCollectionNeedsCollectionStore(t *testing.T) {
	p := collPlugin(t, &fake{meta: Meta{}, setup: func(cx *Registrar) error {
		_, err := cx.Collection("items", specV(1))
		return err
	}})
	a, err := Assemble(Spec{Plugins: entries(p), Inject: map[string]any{ext.ServiceDB: dbHandle{plainStore{}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Start(NewRoot("t")); err == nil || !strings.Contains(err.Error(), "存储不支持集合") {
		t.Fatalf("存储没实现集合应报错：%v", err)
	}
}

// plainStore 实现 store.Store（嵌入的接口为 nil，方法不会被调用）但不实现 CollectionStore。
type plainStore struct{ store.Store }
