package appapi

// 客户端改进 M9：changes.* 与 session.rewind {restore_files} 的编排（真实引擎的端到端用例在模块根 changes_api_test.go）。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/runtime/host"
)

// fakeChanges 还原 = 把 path 写成 "restored"，Undo 写回原内容。
type fakeChanges struct {
	path      string
	prev      []byte
	conflicts []wire.ChangeConflict
	blocked   bool
	reverts   int
	undone    int
}

func (f *fakeChanges) List(context.Context, wire.ChangesArgs, func(string) bool) (wire.ChangesResult, error) {
	return wire.ChangesResult{Files: []wire.ChangeFile{{Path: "a.txt", Status: "modified", Revertible: true}}}, nil
}

func (f *fakeChanges) Diff(context.Context, wire.ChangesDiffArgs, func(string) bool) (wire.ChangesDiffResult, error) {
	return wire.ChangesDiffResult{Path: "a.txt"}, nil
}

func (f *fakeChanges) Revert(_ context.Context, r ChangeRevert) (ChangeReverted, error) {
	out := ChangeReverted{Files: []string{"a.txt"}, Conflicts: f.conflicts, Blocked: f.blocked}
	if f.blocked || r.DryRun {
		return out, nil
	}
	f.reverts++
	prev, err := os.ReadFile(f.path)
	if err != nil {
		return out, err
	}
	f.prev = prev
	if err := os.WriteFile(f.path, []byte("restored"), 0o644); err != nil {
		return out, err
	}
	out.Undo = func(context.Context) error { f.undone++; return os.WriteFile(f.path, f.prev, 0o644) }
	return out, nil
}

func twoTurns(t *testing.T, app *App) int {
	t.Helper()
	if _, e := call2(t, app, "session.create", `{"session":"s"}`); e != nil {
		t.Fatal(e)
	}
	for i := range 2 {
		if _, e := call2(t, app, "session.send", fmt.Sprintf(`{"session":"s","text":"t%d","client_id":"c%d"}`, i, i)); e != nil {
			t.Fatal(e)
		}
		hd, _ := app.Host().Get("s")
		if err := hd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	out, _ := call2(t, app, "session.get", `{"session":"s"}`)
	return out.(wire.SessionSnapshot).Log.Len
}

// 没装 checkpoint：changes.* 与 restore_files 都 unsupported，不报 changes 能力；不带新字段的 rewind 照常。
func TestChangesUnsupported(t *testing.T) {
	app := newApp(t)
	if slices.Contains(app.capabilities(), "changes") {
		t.Fatal("没装 checkpoint 不应报 changes")
	}
	twoTurns(t, app)
	for _, c := range []struct{ cmd, args string }{
		{"changes.list", `{"session":"s"}`},
		{"changes.diff", `{"session":"s","path":"a.txt"}`},
		{"changes.revert", `{"session":"s"}`},
		{"session.rewind", `{"session":"s","to_turn":2,"restore_files":true}`},
	} {
		_, e := call2(t, app, c.cmd, c.args)
		wantCode(t, e, wire.CodeUnsupported, "")
	}
	if r, e := call2(t, app, "session.rewind", `{"session":"s","to_turn":2}`); e != nil || r.(wire.RewindResult).To != 2 || r.(wire.RewindResult).Files != nil {
		t.Fatalf("不带新字段的 rewind：%+v %+v", r, e)
	}
}

// 权限：viewer 能 list / diff，不能 revert；参数校验。
func TestChangesRolesAndArgs(t *testing.T) {
	app := newApp(t)
	app.o.Changes = &fakeChanges{path: filepath.Join(t.TempDir(), "a.txt")}
	if !slices.Contains(app.capabilities(), "changes") {
		t.Fatal("应报 changes 能力")
	}
	twoTurns(t, app)
	v := app.NewConn(context.Background(), wire.Principal{ID: "v", Role: wire.RoleViewer})
	defer v.Close()
	for _, cmd := range []string{"changes.list", "changes.diff"} {
		if _, err := v.Call(context.Background(), cmd, []byte(`{"session":"s","path":"a.txt"}`)); err != nil {
			t.Fatalf("viewer %s：%v", cmd, err)
		}
	}
	_, err := v.Call(context.Background(), "changes.revert", []byte(`{"session":"s"}`))
	var e *Error
	if !errors.As(err, &e) || e.Code != wire.CodeForbidden {
		t.Fatalf("viewer 不能 revert：%v", err)
	}
	for _, args := range []string{`{"session":"s","scope":"turn"}`, `{"session":"s","scope":"x"}`, `{"session":"s","scope":"from_turn","turn":0}`} {
		_, e := call2(t, app, "changes.list", args)
		wantCode(t, e, wire.CodeInvalid, "")
	}
	_, e = call2(t, app, "changes.diff", `{"session":"s"}`)
	wantCode(t, e, wire.CodeInvalid, "")
	_, e = call2(t, app, "changes.list", `{"session":"ghost"}`)
	wantCode(t, e, wire.CodeNotFound, "")
}

// 有冲突：conflict（details.reason=files、details.conflicts），日志和文件都不动。
func TestRewindRestoreConflict(t *testing.T) {
	app := newApp(t)
	p := filepath.Join(t.TempDir(), "a.txt")
	_ = os.WriteFile(p, []byte("orig"), 0o644)
	fc := &fakeChanges{path: p, blocked: true, conflicts: []wire.ChangeConflict{{Path: "a.txt", Reason: "modified", Message: "改过", Forceable: true}}}
	app.o.Changes = fc
	n := twoTurns(t, app)
	_, e := call2(t, app, "session.rewind", `{"session":"s","to_turn":2,"restore_files":true}`)
	wantCode(t, e, wire.CodeConflict, wire.ConflictFiles)
	if cs, _ := e.Details["conflicts"].([]wire.ChangeConflict); len(cs) != 1 || cs[0].Path != "a.txt" {
		t.Fatalf("details.conflicts：%+v", e.Details)
	}
	out, _ := call2(t, app, "session.get", `{"session":"s"}`)
	if l := out.(wire.SessionSnapshot).Log.Len; l != n || fc.reverts != 0 {
		t.Fatalf("冲突时日志 %d→%d、还原 %d 次", n, l, fc.reverts)
	}
	if b, _ := os.ReadFile(p); string(b) != "orig" {
		t.Fatalf("冲突时文件被改：%q", b)
	}
	// changes.revert 同样返回 conflict；dry_run 把冲突放进结果。
	_, e = call2(t, app, "changes.revert", `{"session":"s"}`)
	wantCode(t, e, wire.CodeConflict, wire.ConflictFiles)
	r, e := call2(t, app, "changes.revert", `{"session":"s","dry_run":true}`)
	if e != nil || len(r.(wire.ChangesRevertResult).Conflicts) != 1 {
		t.Fatalf("dry_run：%+v %+v", r, e)
	}
}

// 截断失败：文件写回还原前的内容，返回原错误。
func TestRewindRestoreTruncateFails(t *testing.T) {
	app := newApp(t)
	p := filepath.Join(t.TempDir(), "a.txt")
	_ = os.WriteFile(p, []byte("v2\r\n"), 0o644)
	fc := &fakeChanges{path: p}
	app.o.Changes = fc
	n := twoTurns(t, app)
	old := rewindDo
	rewindDo = func(host.Handle, host.Command) error { return host.ErrStale }
	t.Cleanup(func() { rewindDo = old })
	_, e := call2(t, app, "session.rewind", `{"session":"s","to_turn":2,"restore_files":true}`)
	wantCode(t, e, wire.CodeConflict, wire.ConflictStale)
	if b, _ := os.ReadFile(p); string(b) != "v2\r\n" || fc.reverts != 1 || fc.undone != 1 {
		t.Fatalf("截断失败后文件应写回：%q reverts=%d undone=%d", b, fc.reverts, fc.undone)
	}
	out, _ := call2(t, app, "session.get", `{"session":"s"}`)
	if l := out.(wire.SessionSnapshot).Log.Len; l != n {
		t.Fatalf("日志不应变：%d→%d", n, l)
	}
	// 正常路径：截断并返回还原的文件。
	rewindDo = old
	r, e := call2(t, app, "session.rewind", `{"session":"s","to_turn":2,"restore_files":true}`)
	if e != nil || r.(wire.RewindResult).To != 2 || !slices.Equal(r.(wire.RewindResult).Files, []string{"a.txt"}) {
		t.Fatalf("rewind restore：%+v %+v", r, e)
	}
	if b, _ := os.ReadFile(p); string(b) != "restored" {
		t.Fatalf("应已还原：%q", b)
	}
}
