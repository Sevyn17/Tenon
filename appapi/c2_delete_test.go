package appapi

// C2（Mortise docs/76 D5）：删除创建者时级联停止并删除它的子会话（含异步派出的）；独立会话不受影响。

import (
	"context"
	"testing"

	"github.com/Sevyn17/tenon/runtime/host"
)

func TestDeleteCascadesChildrenNotIndependent(t *testing.T) {
	ctx := context.Background()
	app := newApp(t)
	if _, e := call2(t, app, "session.create", `{"session":"c"}`); e != nil {
		t.Fatal(e)
	}
	h := app.Host()
	child, err := h.StartSession(ctx, host.StartOptions{Creator: "c", Child: true, Task: "子任务", ReplyMode: host.ReplyNone})
	if err != nil {
		t.Fatal(err)
	}
	ind, err := h.StartSession(ctx, host.StartOptions{Creator: "c", Task: "独立任务", ReplyMode: host.ReplyNone})
	if err != nil {
		t.Fatal(err)
	}
	if hd, ok := h.Get(child); ok {
		_ = hd.Wait()
	}
	if hd, ok := h.Get(ind); ok {
		_ = hd.Wait()
	}
	if _, e := call2(t, app, "session.delete", `{"session":"c"}`); e != nil {
		t.Fatal(e)
	}
	if ok, _ := app.o.DB.Exists(ctx, child); ok {
		t.Fatalf("子会话 %s 应随创建者删除", child)
	}
	if ok, _ := app.o.DB.Exists(ctx, ind); !ok {
		t.Fatalf("独立会话 %s 不应随创建者删除", ind)
	}
}
