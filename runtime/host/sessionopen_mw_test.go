package host

// 04 4.3 SessionOpen 中间件在真实 Host 上：拒绝创建、只在新建时跑。

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Sevyn17/tenon/runtime/component"
)

// 拒绝创建：会话不落库，Open 返回错误。
func TestSessionOpenMWDenies(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	r.h.SessionOpenChain = func(ctx context.Context, req component.SessionOpenReq) (component.SessionOpenResp, error) {
		return component.SessionOpenResp{Denied: true, Reason: "测试拒绝"}, nil
	}
	if _, err := r.h.Open(ctx, "blocked"); err == nil {
		t.Fatal("应拒绝建会话")
	}
	// 不落库。
	if ok, _ := r.db.Exists(ctx, "blocked"); ok {
		t.Fatal("被拒绝的会话不该落库")
	}
}

// 只在新建时跑：打开已有会话不触发。
func TestSessionOpenMWOnlyOnCreate(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	calls := 0
	r.h.SessionOpenChain = func(ctx context.Context, req component.SessionOpenReq) (component.SessionOpenResp, error) {
		calls++
		return component.SessionOpenResp{}, nil
	}
	hd, err := r.h.Open(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	_ = hd.Wait()
	if calls != 1 {
		t.Fatalf("新建应触发一次，实际 %d", calls)
	}
	// 会话已存在时再 Open：直接复用 actor，不该再触发。
	if _, err := r.h.Open(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("复用已有会话不该触发，实际 %d 次", calls)
	}
	// OpenExisting 打开已存在的会话：也不该触发。
	if _, err := r.h.OpenExisting(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("OpenExisting 不该触发，实际 %d 次", calls)
	}
}

// 中间件能拿到会话 id 与定义名。
func TestSessionOpenMWSeesContext(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	var got component.SessionOpenReq
	r.h.SessionOpenChain = func(ctx context.Context, req component.SessionOpenReq) (component.SessionOpenResp, error) {
		got = req
		return component.SessionOpenResp{}, nil
	}
	hd, err := r.h.Open(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	_ = hd.Wait()
	if got.SessionID != "s1" {
		t.Fatalf("应拿到会话 id，实际 %q", got.SessionID)
	}
	if got.Origin != "new" {
		t.Fatalf("来源应为 new，实际 %q", got.Origin)
	}
}
