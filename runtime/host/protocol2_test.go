package host

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"tenon/runtime/session"
	"tenon/runtime/store"
)

// protocol 2 G1：OpenExisting 不隐式新建；Create 已存在返回 ErrConflict。
func TestOpenExistingAndCreate(t *testing.T) {
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	ctx := context.Background()
	if _, err := r.h.OpenExisting(ctx, "nope"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("不存在应 ErrSessionNotFound：%v", err)
	}
	if ok, _ := r.db.Exists(ctx, "nope"); ok {
		t.Fatal("OpenExisting 不应新建会话")
	}
	hd, err := r.h.Create(ctx, "c1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.h.Create(ctx, "c1", "", nil); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("重复创建应 ErrConflict：%v", err)
	}
	if got, err := r.h.OpenExisting(ctx, "c1"); err != nil || got.ID() != hd.ID() {
		t.Fatalf("已存在应返回同一句柄：%v", err)
	}
}

// Send 带内容部件时原样进 user 条目（不再只收文本）。
func TestSendContentParts(t *testing.T) {
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	ctx := context.Background()
	hd, err := r.h.Create(ctx, "p", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	parts := session.Content{session.NewTextPart("看图"),
		{Kind: "media", Media: &session.MediaRef{URI: "artifact://abc", MIME: "image/png", Bytes: 3}}}
	if err := hd.Do(Command{Kind: Send, ItemID: "i1", Content: parts}); err != nil {
		t.Fatal(err)
	}
	if err := hd.Wait(); err != nil {
		t.Fatal(err)
	}
	es := hd.Agent().Committed().Entries
	if len(es) == 0 || es[0].Item.User == nil {
		t.Fatalf("应有 user 条目：%d", len(es))
	}
	c := es[0].Item.User.Content
	if len(c) != 2 || c[1].Media == nil || c[1].Media.URI != "artifact://abc" {
		t.Fatalf("部件应原样保留：%+v", c)
	}
}

// IfCursor：游标不符的 Rewind / Fork 返回 ErrStale，日志不变；相符则执行。
func TestIfCursor(t *testing.T) {
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	ctx := context.Background()
	hd, err := r.h.Create(ctx, "s", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"a", "b"} {
		if err := hd.Do(Command{Kind: Send, Text: s}); err != nil {
			t.Fatal(err)
		}
		if err := hd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	l, err := LoadHead(ctx, r.db, "s")
	if err != nil {
		t.Fatal(err)
	}
	stale := l.Cursor - 1
	if err := hd.Do(Command{Kind: Rewind, To: 2, IfCursor: &stale}); !errors.Is(err, ErrStale) {
		t.Fatalf("游标不符应 ErrStale：%v", err)
	}
	if n := hd.Agent().Committed().Header.LogLen; n != 4 {
		t.Fatalf("被拒的 rewind 不应改日志：%d", n)
	}
	if _, err := r.h.ForkIf(ctx, "s", "f", -1, "fork", &stale); !errors.Is(err, ErrStale) {
		t.Fatalf("fork 游标不符应 ErrStale：%v", err)
	}
	cur := l.Cursor
	if err := hd.Do(Command{Kind: Rewind, To: 2, IfCursor: &cur}); err != nil {
		t.Fatalf("游标相符应执行：%v", err)
	}
	if n := hd.Agent().Committed().Header.LogLen; n != 2 {
		t.Fatalf("rewind 后 log_len 应为 2：%d", n)
	}
}

// Rewind{ToTurn}：保留 ToTurn 之前的回合；同一 ToTurn 重发不再截（绝对目标，幂等）。
func TestRewindToTurn(t *testing.T) {
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	ctx := context.Background()
	hd, err := r.h.Create(ctx, "s", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"a", "b", "c"} {
		if err := hd.Do(Command{Kind: Send, Text: s}); err != nil {
			t.Fatal(err)
		}
		if err := hd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	var to int
	if err := hd.Do(Command{Kind: Rewind, ToTurn: 2, RewoundTo: &to}); err != nil {
		t.Fatal(err)
	}
	if to != 2 || hd.Agent().Committed().Header.LogLen != 2 {
		t.Fatalf("应截到回合 2 的起点 2：to=%d len=%d", to, hd.Agent().Committed().Header.LogLen)
	}
	l, _ := LoadHead(ctx, r.db, "s")
	if err := hd.Do(Command{Kind: Rewind, ToTurn: 2, RewoundTo: &to}); err != nil {
		t.Fatalf("重发同一 to_turn 应无害：%v", err)
	}
	l2, _ := LoadHead(ctx, r.db, "s")
	if to != 2 || l2.Cursor != l.Cursor {
		t.Fatalf("重发不应再写事件：to=%d cursor %d → %d", to, l.Cursor, l2.Cursor)
	}
}

// SetConfig 空闲时提交，Committed 置 true。
func TestSetConfigCommitted(t *testing.T) {
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	hd, err := r.h.Create(context.Background(), "s", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	n := uint32(3)
	var ok bool
	if err := hd.Do(Command{Kind: SetConfig, MaxSteps: &n, Committed: &ok}); err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("空闲时 SetConfig 应已提交")
	}
}

// session.forked / session.closed 是持久事件（06 10.5 表一）：forked 与建分叉会话同事务，closed 随关闭的检查点写。
func TestForkedAndClosedEventsDurable(t *testing.T) {
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	ctx := context.Background()
	src, err := r.h.Create(ctx, "src", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := src.Do(Command{Kind: Send, Text: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := src.Wait(); err != nil {
		t.Fatal(err)
	}
	f, err := r.h.Fork(ctx, "src", "f", -1, "fork")
	if err != nil {
		t.Fatal(err)
	}
	evs, _, err := r.db.Replay(ctx, "f", 0)
	if err != nil {
		t.Fatal(err)
	}
	forked := false
	for _, e := range evs {
		if e.Kind == store.EventForked {
			forked = true
		}
	}
	if !forked {
		t.Fatalf("分叉会话应有持久 forked 事件：%d 条", len(evs))
	}
	if err := f.Do(Command{Kind: Close, Text: "bye"}); err != nil {
		t.Fatal(err)
	}
	<-f.Done()
	evs, _, _ = r.db.Replay(ctx, "f", 0)
	if last := evs[len(evs)-1]; last.Kind != store.EventClosed {
		t.Fatalf("关闭应写持久 closed 事件：%s", last.Kind)
	}
}

// session.forked / session.closed 是持久事件（06 10.5 表一）：重连回放能看到，不再只是总线上的易失事件。
func TestForkAndCloseAreDurable(t *testing.T) {
	r := newRig(t, filepath.Join(t.TempDir(), "s.db"), &fakeModel{})
	ctx := context.Background()
	src, err := r.h.Create(ctx, "src", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := src.Do(Command{Kind: Send, Text: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := src.Wait(); err != nil {
		t.Fatal(err)
	}
	f, err := r.h.Fork(ctx, "src", "f1", -1, "fork")
	if err != nil {
		t.Fatal(err)
	}
	kinds := func(id string) []string {
		t.Helper()
		evs, _, err := r.db.Replay(ctx, id, 0)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range evs {
			out = append(out, e.Kind)
		}
		return out
	}
	has := func(ks []string, k string) bool {
		for _, x := range ks {
			if x == k {
				return true
			}
		}
		return false
	}
	if ks := kinds("f1"); !has(ks, store.EventForked) {
		t.Fatalf("分叉会话应有持久的 forked 事件：%v", ks)
	}
	if err := f.Do(Command{Kind: Close, Text: "user"}); err != nil {
		t.Fatal(err)
	}
	<-f.Done()
	if ks := kinds("f1"); !has(ks, store.EventClosed) {
		t.Fatalf("关闭应写持久的 closed 事件：%v", ks)
	}
}
