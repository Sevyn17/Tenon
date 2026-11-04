package ybase

import (
	"context"
	"testing"
	"time"

	"ybase/runtime/store/memstore"
)

// 进程内工件可回收（2026-11-04 审查 P2 资源：原先只增不删）：会话还在的工件留着；会话没了、且过了宽限期的删掉；
// 刚写入的（宽限期内）即使没有引用也留着。
func TestMemArtifactsGC(t *testing.T) {
	ctx := context.Background()
	alive := map[string]bool{"live": true}
	a := newMemArtifacts(func(_ context.Context, id string) (bool, error) { return alive[id], nil })
	now := time.Unix(1000, 0)
	a.now = func() time.Time { return now }
	kept, _ := a.Put(ctx, "live", "text/plain", []byte("kept"))
	dropped, _ := a.Put(ctx, "gone", "text/plain", []byte("dropped"))
	now = now.Add(time.Hour)
	fresh, _ := a.Put(ctx, "", "text/plain", []byte("fresh"))
	n, err := a.GC(ctx, 30*time.Minute)
	if err != nil || n != 1 {
		t.Fatalf("应回收 1 个：n=%d err=%v", n, err)
	}
	if _, err := a.Get(dropped); err == nil {
		t.Error("会话已删的旧工件应回收")
	}
	for _, h := range []string{kept, fresh} {
		if _, err := a.Get(h); err != nil {
			t.Errorf("工件 %s 不应回收：%v", h, err)
		}
	}
}

// SDK 启动后 Host.GC 已接上（assemble.Wire 按工件存储的 GC 方法接线），Maintain 会定期回收。
func TestSDKWiresArtifactGC(t *testing.T) {
	a, err := New(Minimal(), WithStore(memstore.New()), WithWorkdir(t.TempDir()), WithModels(testHub(), "m"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	defer a.Close(ctx)
	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if a.host.GC == nil {
		t.Fatal("SDK 的进程内工件存储应接上 Host.GC")
	}
}
