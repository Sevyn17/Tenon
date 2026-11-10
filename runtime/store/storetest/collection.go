package storetest

// 集合一致性用例（框架设计 05 5.6，T4a）。sqlite 与 memstore 共用，语义以 sqlite 为准。
// 存储没实现 CollectionStore 时跳过：集合是可选能力，与 PruneStore 同一规则。

import (
	"context"
	"errors"
	"testing"

	"github.com/Sevyn17/tenon/runtime/store"
)

func openCollection(t *testing.T, db store.Store, spec store.CollectionSpec) store.Collection {
	t.Helper()
	cs, ok := db.(store.CollectionStore)
	if !ok {
		t.Skip("存储不支持集合")
	}
	spec, err := spec.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	// Open 要求迁移版本与结构版本一致；一致性测试不跑迁移，直接把版本记上。
	if err := cs.SetCollectionVersion(context.Background(), "local", "demo", "items", spec.Version); err != nil {
		t.Fatal(err)
	}
	c, err := cs.Open(context.Background(), "local", "demo", "items", spec)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func demoSpec() store.CollectionSpec {
	return store.CollectionSpec{Indexes: []string{"status"}, Partition: "team"}
}

func putItem(t *testing.T, c store.Collection, part, key, status string, ifVersion int64) {
	t.Helper()
	err := c.Put(context.Background(), part, store.Doc{Key: key, Body: map[string]any{"team": part, "status": status}}, ifVersion)
	if err != nil {
		t.Fatal(err)
	}
}

func testCollectionRoundTrip(t *testing.T, db store.Store) {
	c := openCollection(t, db, demoSpec())
	ctx := context.Background()
	putItem(t, c, "L", "0000000001", "open", 0)
	got, err := c.Get(ctx, "L", "0000000001")
	if err != nil || got.Version != 1 || got.Body["status"] != "open" {
		t.Fatalf("首次写入读回：%+v %v", got, err)
	}
	putItem(t, c, "L", "0000000001", "done", got.Version)
	got, err = c.Get(ctx, "L", "0000000001")
	if err != nil || got.Version != 2 || got.Body["status"] != "done" {
		t.Fatalf("第二次写入读回：%+v %v", got, err)
	}
}

func testCollectionConflict(t *testing.T, db store.Store) {
	c := openCollection(t, db, demoSpec())
	ctx := context.Background()
	_, err := c.Get(ctx, "L", "0000000001")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("不存在应返回 ErrNotFound：%v", err)
	}
	if err := c.Delete(ctx, "L", "0000000001", 0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ifVersion 0 删不存在的 key 应返回 ErrNotFound：%v", err)
	}
	putItem(t, c, "L", "0000000001", "open", 0)
	if err := c.Put(ctx, "L", store.Doc{Key: "0000000001", Body: map[string]any{"team": "L", "status": "x"}}, 0); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("ifVersion 0 对已存在的 key 应冲突：%v", err)
	}
	if err := c.Put(ctx, "L", store.Doc{Key: "0000000001", Body: map[string]any{"team": "L", "status": "x"}}, 7); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("版本不符应冲突：%v", err)
	}
	if err := c.Delete(ctx, "L", "0000000001", 7); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("版本不符的删除应冲突：%v", err)
	}
	if err := c.Put(ctx, "L", store.Doc{Key: "0000000002", Body: map[string]any{"team": "L", "status": "x"}}, 1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("非 0 版本写不存在的 key 应返回 ErrNotFound：%v", err)
	}
	if err := c.Delete(ctx, "L", "0000000001", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(ctx, "L", "0000000001"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("删除后应不存在：%v", err)
	}
}

func testCollectionQueryPage(t *testing.T, db store.Store) {
	c := openCollection(t, db, demoSpec())
	ctx := context.Background()
	for _, k := range []string{"0000000002", "0000000010", "0000000001"} {
		putItem(t, c, "L", k, "open", 0)
	}
	putItem(t, c, "L", "0000000003", "done", 0)
	if _, _, err := c.Query(ctx, "L", "nope", "open", store.Page{}); err == nil {
		t.Fatal("未声明的索引应报错")
	}
	var got []string
	page := store.Page{Limit: 2}
	for {
		docs, next, err := c.Query(ctx, "L", "status", "open", page)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range docs {
			got = append(got, d.Key)
		}
		if next.After == "" {
			break
		}
		page.After = next.After
	}
	want := []string{"0000000001", "0000000002", "0000000010"}
	if len(got) != len(want) {
		t.Fatalf("分页应不重不漏：%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("应按 Key 升序：%v", got)
		}
	}
}

func testCollectionPartition(t *testing.T, db store.Store) {
	c := openCollection(t, db, demoSpec())
	ctx := context.Background()
	putItem(t, c, "L", "0000000001", "open", 0)
	if _, err := c.Get(ctx, "M", "0000000001"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("一个分区读不到另一个分区：%v", err)
	}
	err := c.Put(ctx, "L", store.Doc{Key: "0000000002", Body: map[string]any{"team": "M", "status": "open"}}, 0)
	if err == nil {
		t.Fatal("正文的分区字段与 part 不符应报错")
	}
}

func testCollectionTx(t *testing.T, db store.Store) {
	ctx := context.Background()
	cs := db.(store.CollectionStore)
	spec, err := demoSpec().Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if err := cs.SetCollectionVersion(ctx, "local", "demo", "items", spec.Version); err != nil {
		t.Fatal(err)
	}
	err = db.Tx(ctx, func(tx store.Store) error {
		c, err := tx.(store.CollectionStore).Open(ctx, "local", "demo", "items", demoSpec())
		if err != nil {
			return err
		}
		return c.Put(ctx, "L", store.Doc{Key: "0000000001", Body: map[string]any{"team": "L", "status": "open"}}, 0)
	})
	if err != nil {
		t.Fatal(err)
	}
	c, err := cs.Open(ctx, "local", "demo", "items", demoSpec())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(ctx, "L", "0000000001"); err != nil {
		t.Fatalf("提交后应可见：%v", err)
	}
	err = db.Tx(ctx, func(tx store.Store) error {
		c, err := tx.(store.CollectionStore).Open(ctx, "local", "demo", "items", demoSpec())
		if err != nil {
			return err
		}
		if err := c.Put(ctx, "L", store.Doc{Key: "0000000002", Body: map[string]any{"team": "L", "status": "open"}}, 0); err != nil {
			return err
		}
		return errors.New("回滚")
	})
	if err == nil {
		t.Fatal("事务应返回错误")
	}
	if _, err := c.Get(ctx, "L", "0000000002"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("回滚后应不可见：%v", err)
	}
}

func testCollectionSpecStable(t *testing.T, db store.Store) {
	c := openCollection(t, db, demoSpec())
	putItem(t, c, "L", "0000000001", "open", 0)
	cs := db.(store.CollectionStore)
	other := demoSpec()
	other.Indexes = []string{"owner"}
	if _, err := cs.Open(context.Background(), "local", "demo", "items", other); err == nil {
		t.Fatal("同名再次打开且结构不同应报错")
	}
	if _, err := c.Get(context.Background(), "L", "0000000001"); err != nil {
		t.Fatalf("结构不符的打开不应动原数据：%v", err)
	}
}
