package agentdef

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ybase/runtime/store/memstore"
)

func defFiles(id string) Files {
	return Files{ManifestName: []byte("format = \"ybase/v1\"\nid = \"" + id + "\"\ndescription = \"测试定义\"\n")}
}

// 存储定义来源：Save/Load 往返、版本递增、按 scope 列举、租户隔离。
func TestStoreSourceRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := memstore.New()
	src := NewStoreSource(db, "t1", "")
	refs := []Ref{
		{Namespace: Platform, Name: "app"},
		{Namespace: Tenant, Tenant: "t1", Name: "tenant-a"},
		{Namespace: User, Tenant: "t1", User: "u1", Name: "user-a"},
	}
	for _, ref := range refs {
		v, h, err := src.Save(ctx, ref, defFiles(ref.Name))
		if err != nil {
			t.Fatalf("Save %s：%v", ref, err)
		}
		if v != 1 || h == "" {
			t.Fatalf("Save %s 版本/哈希 = %d %q", ref, v, h)
		}
		a, err := src.Load(ctx, ref)
		if err != nil {
			t.Fatalf("Load %s：%v", ref, err)
		}
		if a.ID != ref.Name || a.Version != h {
			t.Fatalf("Load %s = id %q version %q，期望哈希 %q", ref, a.ID, a.Version, h)
		}
		if v2, _, err := src.Save(ctx, ref, defFiles(ref.Name)); err != nil || v2 != 2 {
			t.Fatalf("第二个版本 %s：%d %v", ref, v2, err)
		}
	}

	list := func(scope Scope) []string {
		t.Helper()
		got, err := src.List(ctx, scope)
		if err != nil {
			t.Fatalf("List %+v：%v", scope, err)
		}
		out := make([]string, len(got))
		for i, r := range got {
			out[i] = r.FullName()
		}
		return out
	}
	if got := strings.Join(list(Scope{Tenant: "t1"}), ","); got != "app,tenant-a" {
		t.Fatalf("租户层列举 = %s", got)
	}
	if got := strings.Join(list(Scope{Tenant: "t1", User: "u1"}), ","); got != "app,tenant-a,user/u1/user-a" {
		t.Fatalf("用户层列举 = %s", got)
	}
	// 平台定义对所有租户可见，租户层定义不越界。
	if got := strings.Join(list(Scope{Tenant: "t2"}), ","); got != "app" {
		t.Fatalf("别的租户应只看到平台定义：%s", got)
	}

	// 别的租户的引用按不存在处理（不泄露是否存在）。
	if _, err := src.Load(ctx, Ref{Namespace: Tenant, Tenant: "t2", Name: "tenant-a"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("别的租户：%v", err)
	}
	if _, err := src.Load(ctx, Ref{Namespace: Platform, Name: "nope"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的平台定义：%v", err)
	}
}

// 存储里出现无法解析成引用的键（老版本 / 手写数据）时列举跳过、不报错。
func TestStoreSourceSkipsBadKeys(t *testing.T) {
	ctx := context.Background()
	db := memstore.New()
	src := NewStoreSource(db, "t1", "")
	if _, _, err := src.Save(ctx, Ref{Namespace: Platform, Name: "app"}, defFiles("app")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.PutDefinition(ctx, "agent", "platform/Bad Name", string(Encode(defFiles("x")))); err != nil {
		t.Fatal(err)
	}
	got, err := src.List(ctx, Scope{Tenant: "t1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "app" {
		t.Fatalf("非法键应被跳过：%+v", got)
	}
	if _, err := src.Load(ctx, Ref{Namespace: Platform, Name: "Bad Name"}); err == nil {
		t.Fatal("非法名字应报错")
	}
}

// 定义包里的 id 与引用名字不一致时拒绝写入。
func TestStoreSourceSaveIDMismatch(t *testing.T) {
	ctx := context.Background()
	src := NewStoreSource(memstore.New(), "t1", "")
	if _, _, err := src.Save(ctx, Ref{Namespace: Platform, Name: "other"}, defFiles("mismatch")); err == nil {
		t.Fatal("id 与名字不一致应报错")
	}
	// 定义类缺省 agent；显式给 kind 时互不干扰。
	db := memstore.New()
	if _, _, err := NewStoreSource(db, "t1", "tool").Save(ctx, Ref{Namespace: Platform, Name: "t"}, defFiles("t")); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStoreSource(db, "t1", "").Load(ctx, Ref{Namespace: Platform, Name: "t"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("定义类隔离：%v", err)
	}
}
