package checkpoint

import (
	"context"
	"testing"

	"github.com/Sevyn17/tenon/runtime/store/memstore"
)

// T-4：LastByPath 跨会话取本工作区同一文件 Seq 最大的一条，忽略其他工作区的记录；没有记录返回 nil。
func TestLastByPath(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	e := New(root, openColl(t, memstore.New()), nil)
	key := PathKey("a.txt")
	add := func(k, session, root, seq, after string) {
		t.Helper()
		c := Change{Key: k, Session: session, Source: SourceFS, Root: root, Path: "a.txt", PathKey: key, After: after, Seq: seq}
		if err := e.put(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := e.LastByPath(ctx, key); err != nil || got != nil {
		t.Fatalf("没有记录时 = %v, %v，期望 nil", got, err)
	}
	mine := e.Root
	add("k1", "s1", mine, "00000000000000000001", "h1")
	add("k2", "s2", mine, "00000000000000000002", "h2")
	add("k3", "s3", "/other/root", "00000000000000000003", "h3") // 其他工作区，较新也不算
	got, err := e.LastByPath(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Session != "s2" || got.After != "h2" {
		t.Fatalf("LastByPath = %+v，期望 s2/h2", got)
	}
}
