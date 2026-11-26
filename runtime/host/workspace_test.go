package host

import (
	"context"
	"errors"
	"testing"

	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
)

// 客户端改进 M6 协议T5（X-01）：CLI 走 Open 新建的会话要记工作区与来源，和 Create 一样。
// 此前 Open 只带会话 id，会话行 workspace 为空（origin 靠存储缺省填 new）：网页端按工作区列不到，别的项目也能打开它。
func TestOpenNewRecordsWorkspaceAndOrigin(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, nil, &fakeModel{})
	r.h.Workspace = "wa"
	if _, err := r.h.Open(ctx, "cli-1"); err != nil {
		t.Fatal(err)
	}
	l, err := r.db.Load(ctx, "cli-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if l.Meta.Workspace != "wa" || l.Meta.Origin != "new" {
		t.Fatalf("Open 新建的会话应带 workspace=wa、origin=new：%+v", l.Meta)
	}
	// 按工作区列会话（sessions.list 缺省走这条）能看到它。
	ls, err := r.db.ListSessionsIn(ctx, "wa", 0, 50, true)
	if err != nil || len(ls) != 1 || ls[0].ID != "cli-1" || ls[0].Origin != "new" {
		t.Fatalf("工作区 wa 的列表应含 cli-1：%+v %v", ls, err)
	}

	// 另一个工作区的宿主（同一份库）打开它：报属于另一个工作区。
	r2 := newRig(t, r.db, &fakeModel{})
	r2.h.Workspace = "wb"
	if _, err := r2.h.Open(ctx, "cli-1"); !errors.Is(err, ErrOtherWorkspace) {
		t.Fatalf("别的工作区打开应报 ErrOtherWorkspace，得到 %v", err)
	}
}

// 已有会话走 Open 恢复：会话行不变。没记工作区的旧会话照常打开，也不被改写成当前工作区（docs/50 决定 10）。
func TestOpenExistingKeepsMeta(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, nil, &fakeModel{})
	r.h.Workspace = "wa"
	c := session.New(&session.Config{Model: "m", Policy: session.DefaultPolicy()})
	// 存储对空 origin 缺省填 new；这里给 fork 以便看出字段没被 Open 的新建字段覆盖。
	if err := r.db.CreateSession(ctx, store.SessionMeta{Workspace: "wa", ID: "old", Origin: "fork", Title: "旧"}, c.Snapshot().Header, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := r.h.Open(ctx, "old"); err != nil {
		t.Fatal(err)
	}
	l, err := r.db.Load(ctx, "old", nil)
	if err != nil {
		t.Fatal(err)
	}
	if l.Meta.Workspace != "wa" || l.Meta.Origin != "fork" || l.Meta.Title != "旧" {
		t.Fatalf("旧会话的会话行不应被改写：%+v", l.Meta)
	}
}

// Mortise docs/63 W-4：跨工作区分叉在建行之前就拒绝，不留孤儿会话；同工作区分叉继承来源工作区。
func TestForkChecksWorkspaceBeforeCreate(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, nil, &fakeModel{})
	r.h.Workspace = "wa"
	c := session.New(&session.Config{Model: "m", Policy: session.DefaultPolicy()})
	if err := r.db.CreateSession(ctx, store.SessionMeta{ID: "src", Workspace: "wa"}, c.Snapshot().Header, nil); err != nil {
		t.Fatal(err)
	}

	// 另一个工作区的宿主（同一份库）分叉：报 ErrOtherWorkspace，且不建行。
	r2 := newRig(t, r.db, &fakeModel{})
	r2.h.Workspace = "wb"
	if _, err := r2.h.Fork(ctx, "src", "orphan", -1, "fork"); !errors.Is(err, ErrOtherWorkspace) {
		t.Fatalf("跨工作区分叉应报 ErrOtherWorkspace，得到 %v", err)
	}
	if ok, err := r.db.Exists(ctx, "orphan"); err != nil || ok {
		t.Fatalf("跨工作区分叉被拒后不应留下会话行：exists=%v err=%v", ok, err)
	}

	// 同工作区分叉：成功，新会话继承来源工作区。
	if _, err := r.h.Fork(ctx, "src", "child", -1, "fork"); err != nil {
		t.Fatal(err)
	}
	l, err := r.db.Load(ctx, "child", nil)
	if err != nil {
		t.Fatal(err)
	}
	if l.Meta.Workspace != "wa" || l.Meta.ParentID != "src" {
		t.Fatalf("分叉应继承来源工作区 wa：%+v", l.Meta)
	}
}
