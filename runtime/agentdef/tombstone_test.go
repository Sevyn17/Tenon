package agentdef

import (
	"context"
	"errors"
	"testing"

	"github.com/Sevyn17/tenon/runtime/store/memstore"
)

func descFiles(id, desc string) Files {
	return Files{ManifestName: []byte("format = \"tenon/v1\"\nid = \"" + id + "\"\ndescription = \"" + desc + "\"\n")}
}

// T-2：租户层副本写墓碑后，Load 按不存在处理、List 跳过，名字解析回落到平台层；旧版本保留。
func TestStoreSourceTombstone(t *testing.T) {
	ctx := context.Background()
	db := memstore.New()
	src := NewStoreSource(db, "t1", "")
	platform := Mem()
	if err := platform.Put(Ref{Namespace: Platform, Name: "main"}, descFiles("main", "内置")); err != nil {
		t.Fatal(err)
	}
	tref := Ref{Namespace: Tenant, Tenant: "t1", Name: "main"}
	_, copyHash, err := src.Save(ctx, tref, descFiles("main", "副本"))
	if err != nil {
		t.Fatal(err)
	}
	all := Multi(src, platform)
	scope := Scope{Tenant: "t1"}

	a, _, err := Resolve(ctx, all, scope, "main")
	if err != nil || a.Description != "副本" {
		t.Fatalf("有副本时应解析到副本：%+v %v", a, err)
	}

	v, err := src.Delete(ctx, tref)
	if err != nil || v != 2 {
		t.Fatalf("写墓碑：v=%d err=%v", v, err)
	}
	if _, err := src.Load(ctx, tref); !errors.Is(err, ErrNotFound) {
		t.Fatalf("墓碑后 Load 应 ErrNotFound：%v", err)
	}
	refs, err := src.List(ctx, scope)
	if err != nil || len(refs) != 0 {
		t.Fatalf("墓碑后 List 应为空：%v %v", refs, err)
	}
	a, ref, err := Resolve(ctx, all, scope, "main")
	if err != nil || a.Description != "内置" || ref.Namespace != Platform {
		t.Fatalf("墓碑后应回落到平台层：%+v %v %v", a, ref, err)
	}
	// 重复删除：已是墓碑，报不存在，不再写版本。
	if _, err := src.Delete(ctx, tref); !errors.Is(err, ErrNotFound) {
		t.Fatalf("重复删除应 ErrNotFound：%v", err)
	}
	// 旧版本仍可按哈希找回（审计）。
	old, err := src.ByHash(ctx, copyHash)
	if err != nil || old.Description != "副本" || old.Version != copyHash {
		t.Fatalf("按哈希找回旧版本：%+v %v", old, err)
	}
	// 再保存一次即"重新复制"，墓碑之后照常生效。
	if _, _, err := src.Save(ctx, tref, descFiles("main", "副本2")); err != nil {
		t.Fatal(err)
	}
	if a, _, _ := Resolve(ctx, all, scope, "main"); a.Description != "副本2" {
		t.Fatalf("墓碑后重新保存应生效：%+v", a)
	}
	if _, err := src.ByHash(ctx, "0000"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的哈希：%v", err)
	}
	if _, err := src.Delete(ctx, Ref{Namespace: Tenant, Tenant: "t1", Name: "nope"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删除不存在的定义：%v", err)
	}
}
