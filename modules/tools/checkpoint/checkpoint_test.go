package checkpoint

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/store"
	"github.com/Sevyn17/tenon/runtime/store/memstore"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// —— 测试替身 ——//

type fakeArt struct {
	mu   sync.Mutex
	m    map[string][]byte
	refs map[string]map[string]bool
}

func (a *fakeArt) Put(_ context.Context, sid, _ string, data []byte) (string, error) {
	sum := sha256.Sum256(data)
	h := hex.EncodeToString(sum[:])
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.m == nil {
		a.m, a.refs = map[string][]byte{}, map[string]map[string]bool{}
	}
	a.m[h] = append([]byte(nil), data...)
	if a.refs[h] == nil {
		a.refs[h] = map[string]bool{}
	}
	a.refs[h][sid] = true
	return h, nil
}

func (a *fakeArt) Get(h string) ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	b, ok := a.m[h]
	if !ok {
		return nil, errors.New("not found")
	}
	return append([]byte(nil), b...), nil
}

type fakeScope struct{ turn uint32 }

func (s *fakeScope) ID() string               { return "" }
func (s *fakeScope) Value(string) (any, bool) { return nil, false }
func (s *fakeScope) SetValue(string, any)     {}
func (s *fakeScope) History() (int, []*session.Entry) {
	if s.turn == 0 {
		return 0, nil
	}
	return 0, []*session.Entry{{Turn: s.turn - 1}, {Turn: s.turn}}
}

func openColl(t *testing.T, db *memstore.Store) store.Collection {
	t.Helper()
	ctx := context.Background()
	if err := db.SetCollectionVersion(ctx, store.Tenant, Name, "changes", 1); err != nil {
		t.Fatal(err)
	}
	c, err := db.Open(ctx, store.Tenant, Name, "changes", CollectionSpec)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

type harness struct {
	t     *testing.T
	e     *Engine
	art   *fakeArt
	root  string
	scope *fakeScope
	n     int
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	art := &fakeArt{}
	root := t.TempDir()
	e := New(root, openColl(t, memstore.New()), func() toolhub.Artifacts { return art })
	return &harness{t: t, e: e, art: art, root: root, scope: &fakeScope{turn: 1}}
}

func (h *harness) abs(rel string) string { return filepath.Join(h.root, filepath.FromSlash(rel)) }

func (h *harness) put(rel, s string) {
	h.t.Helper()
	if err := os.MkdirAll(filepath.Dir(h.abs(rel)), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(h.abs(rel), []byte(s), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) get(rel string) (string, bool) {
	b, err := os.ReadFile(h.abs(rel))
	if err != nil {
		return "", false
	}
	return string(b), true
}

func (h *harness) mustBe(rel, want string) {
	h.t.Helper()
	got, ok := h.get(rel)
	if !ok || got != want {
		h.t.Fatalf("%s = %q(存在=%v)，期望 %q", rel, got, ok, want)
	}
}

func (h *harness) gone(rel string) {
	h.t.Helper()
	if _, err := os.Lstat(h.abs(rel)); !os.IsNotExist(err) {
		h.t.Fatalf("%s 应不存在：%v", rel, err)
	}
}

// call 以 write_path 工具身份走一次中间件；run 是工具本身。
func (h *harness) callAs(sid, callID, rel string, run func() toolhub.Output) toolhub.Output {
	h.t.Helper()
	args, _ := json.Marshal(map[string]string{"path": rel})
	req := component.ToolCallReq{Tool: "write_file", Args: args, SessionID: sid, Risk: toolhub.Write, CallID: callID,
		Attrs: map[string]any{toolhub.AttrWritePath: "path"}, Scope: h.scope}
	resp, err := h.e.Middleware()(context.Background(), req,
		func(context.Context, component.ToolCallReq) (component.ToolCallResp, error) {
			return component.ToolCallResp{Output: run()}, nil
		})
	if err != nil {
		h.t.Fatal(err)
	}
	return resp.Output
}

func (h *harness) write(sid, rel, s string) {
	h.t.Helper()
	h.n++
	h.callAs(sid, "c"+itoa(h.n), rel, func() toolhub.Output { h.put(rel, s); return toolhub.Text("ok") })
}

func (h *harness) remove(sid, rel string) {
	h.t.Helper()
	h.n++
	h.callAs(sid, "c"+itoa(h.n), rel, func() toolhub.Output { _ = os.Remove(h.abs(rel)); return toolhub.Text("ok") })
}

func itoa(n int) string { return strconv.Itoa(n) }

// link 建目录链接：Windows 用 junction（普通用户可建），其他平台用符号链接。
func link(target, at string) error {
	if runtime.GOOS == "windows" {
		if out, err := exec.Command("cmd", "/c", "mklink", "/J", at, target).CombinedOutput(); err != nil {
			return fmt.Errorf("%v: %s", err, out)
		}
		return nil
	}
	return os.Symlink(target, at)
}

func (h *harness) list(sid string) []Change {
	h.t.Helper()
	cs, err := h.e.List(context.Background(), sid)
	if err != nil {
		h.t.Fatal(err)
	}
	return cs
}

func sum(s string) string {
	x := sha256.Sum256([]byte(s))
	return hex.EncodeToString(x[:])
}

func sources(cs []Change) string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Source)
	}
	return strings.Join(out, ",")
}

// —— 第二、三节：记录 ——//

func TestRecordCreateAndOverwrite(t *testing.T) {
	h := newHarness(t)
	h.write("s1", "src/a.go", "v1\n")
	h.write("s1", "src/a.go", "v2\n")
	cs := h.list("s1")
	if len(cs) != 2 {
		t.Fatalf("应有两条记录：%+v", cs)
	}
	if cs[0].Before != "" || cs[0].After != sum("v1\n") || !cs[0].Revertible || cs[0].Turn != 1 || cs[0].Path != "src/a.go" {
		t.Fatalf("新建记录不对：%+v", cs[0])
	}
	if cs[1].Before != sum("v1\n") || cs[1].After != sum("v2\n") || !cs[1].BeforeKept || cs[1].Source != SourceFS {
		t.Fatalf("覆盖记录不对：%+v", cs[1])
	}
	if b, _ := h.e.Content(cs[1].Before); string(b) != "v1\n" {
		t.Fatalf("原文应存进工件：%q", b)
	}
}

func TestRecordUnchangedAndFailures(t *testing.T) {
	h := newHarness(t)
	h.put("a.txt", "x")
	// 工具失败、没写盘（如 edit 匹配失败、被拒绝）：不记。
	h.callAs("s1", "c1", "a.txt", func() toolhub.Output { return toolhub.Failf("no match") })
	// 工具报错但磁盘已变：照记。
	h.callAs("s1", "c2", "a.txt", func() toolhub.Output { h.put("a.txt", "y"); return toolhub.Failf("half") })
	cs := h.list("s1")
	if len(cs) != 1 || cs[0].CallID != "c2" {
		t.Fatalf("只应记 c2：%+v", cs)
	}
}

func TestConfirmRecordedOnce(t *testing.T) {
	h := newHarness(t)
	h.callAs("s1", "c1", "a.txt", func() toolhub.Output { return toolhub.Output{Kind: toolhub.Confirm, Reason: "?"} })
	if n := len(h.list("s1")); n != 0 {
		t.Fatalf("Confirm 不应记：%d", n)
	}
	// 批准后整条链重跑。
	h.callAs("s1", "c1", "a.txt", func() toolhub.Output { h.put("a.txt", "z"); return toolhub.Text("ok") })
	if cs := h.list("s1"); len(cs) != 1 || cs[0].CallID != "c1" {
		t.Fatalf("批准后应只记一次：%+v", cs)
	}
}

func TestRunCodeSubcallsNotDuplicated(t *testing.T) {
	h := newHarness(t)
	mw := h.e.Middleware()
	outer := component.ToolCallReq{Tool: "run_code", Args: json.RawMessage(`{"code":"..."}`), SessionID: "s1",
		Risk: toolhub.Dangerous, CallID: "c7", Scope: h.scope}
	_, err := mw(context.Background(), outer, func(context.Context, component.ToolCallReq) (component.ToolCallResp, error) {
		h.callAs("s1", "c7#1", "a.txt", func() toolhub.Output { h.put("a.txt", "1"); return toolhub.Text("ok") })
		h.callAs("s1", "c7#2", "b.txt", func() toolhub.Output { h.put("b.txt", "2"); return toolhub.Text("ok") })
		return component.ToolCallResp{Output: toolhub.Text("done")}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	cs := h.list("s1")
	if len(cs) != 2 || cs[0].CallID != "c7#1" || cs[1].CallID != "c7#2" {
		t.Fatalf("应只记两次子调用：%+v", cs)
	}
}

func TestDriftRecorded(t *testing.T) {
	h := newHarness(t)
	h.write("s1", "a.txt", "1")
	h.put("a.txt", "user edit") // 用户在编辑器里改过
	h.scope.turn = 2
	h.write("s1", "a.txt", "2")
	cs := h.list("s1")
	if sources(cs) != "fs,drift,fs" {
		t.Fatalf("应补一条 drift：%s", sources(cs))
	}
	if d := cs[1]; d.Before != sum("1") || d.After != sum("user edit") || d.Turn != 2 || d.CallID != "turn-2/drift" {
		t.Fatalf("drift 记录不对：%+v", d)
	}
	if cs[2].Before != sum("user edit") {
		t.Fatalf("工具记录的 before 应是用户改后的内容：%+v", cs[2])
	}
}

func TestAfterTurnReconcile(t *testing.T) {
	h := newHarness(t)
	h.write("s1", "a.txt", "1")
	h.put("a.txt", "late") // 后台进程在调用返回后才写
	h.e.AfterTurn("s1", session.EndReason{})
	cs := h.list("s1")
	if sources(cs) != "fs,drift" || cs[1].After != sum("late") || cs[1].Turn != 1 {
		t.Fatalf("回合末应补 drift：%+v", cs)
	}
	// 再对账一次：没碰过的会话、没变化的文件都不补。
	h.e.AfterTurn("s1", session.EndReason{})
	if n := len(h.list("s1")); n != 2 {
		t.Fatalf("不应重复补：%d", n)
	}
}

func TestMaxFileBytes(t *testing.T) {
	h := newHarness(t)
	h.e.MaxFileBytes = 8
	h.put("big.txt", strings.Repeat("x", 20))
	h.write("s1", "big.txt", strings.Repeat("y", 20))
	cs := h.list("s1")
	if len(cs) != 1 || cs[0].Revertible || !strings.Contains(cs[0].Why, "太大") || cs[0].BeforeSize != 20 {
		t.Fatalf("超过单文件上限应记为不可还原：%+v", cs)
	}
}

func TestSessionQuota(t *testing.T) {
	h := newHarness(t)
	h.e.MaxSessionBytes = 10
	var notes []string
	h.e.Notify = func(_, msg string) { notes = append(notes, msg) }
	h.write("s1", "a.txt", "0123456789abc")
	h.write("s1", "a.txt", "more content here")
	h.write("s1", "a.txt", "and more")
	cs := h.list("s1")
	if len(cs) != 3 {
		t.Fatalf("配额满后照常记录：%d", len(cs))
	}
	if cs[0].AfterKept || cs[1].BeforeKept || cs[1].Revertible {
		t.Fatalf("配额满后不应再存内容：%+v", cs[1])
	}
	if len(notes) != 1 {
		t.Fatalf("配额满只提示一次：%v", notes)
	}
	// 另一个会话不受影响。
	h.write("s2", "b.txt", "x")
	if cs := h.list("s2"); len(cs) != 1 || !cs[0].AfterKept {
		t.Fatalf("配额按会话算：%+v", cs)
	}
}

func TestDeleteSessionClears(t *testing.T) {
	h := newHarness(t)
	h.write("s1", "a.txt", "1")
	h.write("s1", "a.txt", "2")
	h.write("s2", "b.txt", "1")
	if err := h.e.DeleteSession(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
	if n := len(h.list("s1")); n != 0 {
		t.Fatalf("删会话后集合应空：%d", n)
	}
	if n := len(h.list("s2")); n != 1 {
		t.Fatalf("别的会话不受影响：%d", n)
	}
}

func TestTurnOf(t *testing.T) {
	if TurnOf(nil) != 0 || TurnOf(&fakeScope{}) != 0 || TurnOf(&fakeScope{turn: 5}) != 5 {
		t.Fatal("回合号应取已提交历史最后一条")
	}
}

func TestPathKeyAndRootCase(t *testing.T) {
	if pathKeyFor("windows", `Src\A.go`) != "src/a.go" || pathKeyFor("darwin", "Src/A.go") != "src/a.go" ||
		pathKeyFor("linux", "Src/A.go") != "Src/A.go" {
		t.Fatal("path_key：Windows / macOS 小写、正斜杠；Linux 保留大小写")
	}
	if !sameRootFor("windows", `C:\Proj\a`, `c:\proj\A`) || !sameRootFor("darwin", "/Users/X/p", "/users/x/P/") {
		t.Fatal(`C:\Proj\a 与 c:\proj\A 应视为同一路径`)
	}
	if sameRootFor("linux", "/home/X", "/home/x") {
		t.Fatal("Linux 区分大小写")
	}
}

// —— 第九节：diff ——//

func TestDiff(t *testing.T) {
	d := Diff("a.txt", []byte("one\r\ntwo\r\nthree\r\n"), []byte("one\r\nTWO\r\nthree\r\nfour\r\n"))
	if d.Added != 2 || d.Removed != 1 || !strings.HasPrefix(d.Unified, "--- a/a.txt\n+++ b/a.txt\n") ||
		!strings.Contains(d.Unified, "-two\r\n") || !strings.Contains(d.Unified, "+TWO\r\n") {
		t.Fatalf("CRLF diff：%+v\n%s", d, d.Unified)
	}
	d = Diff("n.txt", nil, []byte("x"))
	if !strings.HasPrefix(d.Unified, "--- /dev/null\n+++ b/n.txt\n") || d.Added != 1 ||
		!strings.Contains(d.Unified, "No newline at end of file") {
		t.Fatalf("新建：%s", d.Unified)
	}
	d = Diff("n.txt", []byte("x\n"), nil)
	if !strings.Contains(d.Unified, "+++ /dev/null") || d.Removed != 1 {
		t.Fatalf("删除：%s", d.Unified)
	}
	d = Diff("b.bin", []byte("a\x00b"), []byte("a\x00c!"))
	if !d.Binary || d.Unified != "" || d.OldSize != 3 || d.NewSize != 4 {
		t.Fatalf("二进制：%+v", d)
	}
	if d = Diff("s.txt", []byte("same"), []byte("same")); d.Unified != "" || d.Added+d.Removed != 0 {
		t.Fatalf("相同：%+v", d)
	}
}

// —— 第四、八节：还原 ——//

func revert(t *testing.T, h *harness, o RevertOptions) (RevertResult, error) {
	t.Helper()
	if o.Session == "" {
		o.Session = "s1"
	}
	return h.e.Revert(context.Background(), o)
}

func TestRevertSessionScope(t *testing.T) {
	h := newHarness(t)
	h.put("mod.txt", "\xEF\xBB\xBForig\r\nline2") // BOM、CRLF、无结尾换行
	h.put("del.txt", "bye\n")
	h.put("bin.dat", "a\x00b\x00")
	h.write("s1", "new.txt", "n\n")
	h.write("s1", "mod.txt", "changed\n")
	h.write("s1", "mod.txt", "changed again\n")
	h.remove("s1", "del.txt")
	h.write("s1", "bin.dat", "\x00\x01")
	res, err := revert(t, h, RevertOptions{})
	if err != nil || !res.Applied || len(res.Files) != 4 {
		t.Fatalf("还原：%v %+v", err, res)
	}
	h.gone("new.txt")
	h.mustBe("mod.txt", "\xEF\xBB\xBForig\r\nline2")
	h.mustBe("del.txt", "bye\n")
	h.mustBe("bin.dat", "a\x00b\x00")
	// 还原后补了 revert 记录：之后的工具调用不报 drift。
	h.write("s1", "mod.txt", "next\n")
	if s := sources(h.list("s1")); strings.Contains(s, "drift") {
		t.Fatalf("还原后不应出现 drift：%s", s)
	}
}

func TestRevertTurnScopes(t *testing.T) {
	h := newHarness(t)
	h.put("f.txt", "a")
	h.write("s1", "f.txt", "b")
	h.scope.turn = 2
	h.write("s1", "f.txt", "c")
	// 只撤第 1 回合：第 2 回合也改过 → 冲突，不写。
	res, err := revert(t, h, RevertOptions{Scope: ScopeTurn, Turn: 1})
	if !errors.Is(err, ErrConflict) || len(res.Conflicts) != 1 || res.Conflicts[0].Kind != ConflictLaterTurn {
		t.Fatalf("turn 范围应报后续回合冲突：%v %+v", err, res)
	}
	h.mustBe("f.txt", "c")
	// 从第 2 回合起：回到 b。
	if _, err := revert(t, h, RevertOptions{Scope: ScopeFromTurn, Turn: 2}); err != nil {
		t.Fatal(err)
	}
	h.mustBe("f.txt", "b")
	// 再从第 1 回合起：回到 a。
	if _, err := revert(t, h, RevertOptions{Scope: ScopeFromTurn, Turn: 1}); err != nil {
		t.Fatal(err)
	}
	h.mustBe("f.txt", "a")
}

func TestRevertTurnOnly(t *testing.T) {
	h := newHarness(t)
	h.put("f.txt", "a")
	h.put("g.txt", "g0")
	h.write("s1", "f.txt", "b")
	h.scope.turn = 2
	h.write("s1", "g.txt", "g1")
	if _, err := revert(t, h, RevertOptions{Scope: ScopeTurn, Turn: 2}); err != nil {
		t.Fatal(err)
	}
	h.mustBe("f.txt", "b")
	h.mustBe("g.txt", "g0")
}

func snapshotDir(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err == nil && fi.Mode().IsRegular() {
			b, _ := os.ReadFile(p)
			out[p] = string(b)
		}
		return nil
	})
	return out
}

func sameSnap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestRevertConflictWritesNothing(t *testing.T) {
	h := newHarness(t)
	h.put("f.txt", "a")
	h.put("g.txt", "g0")
	h.write("s1", "f.txt", "b")
	h.write("s1", "g.txt", "g1")
	h.write("s1", "n.txt", "new")
	h.put("f.txt", "external") // 外部程序改过
	before := snapshotDir(t, h.root)
	res, err := revert(t, h, RevertOptions{})
	if !errors.Is(err, ErrConflict) || res.Applied || len(res.Conflicts) != 1 || res.Conflicts[0].Kind != ConflictModified ||
		!res.Conflicts[0].Forceable {
		t.Fatalf("应报外部改动冲突：%v %+v", err, res)
	}
	if !sameSnap(before, snapshotDir(t, h.root)) {
		t.Fatal("冲突时一个字节都不应写")
	}
	// 演练：不写。
	if res, err := revert(t, h, RevertOptions{DryRun: true, Force: true}); err != nil || res.Applied || len(res.Files) != 3 {
		t.Fatalf("演练：%v %+v", err, res)
	}
	if !sameSnap(before, snapshotDir(t, h.root)) {
		t.Fatal("演练不应写盘")
	}
	// 强制：写，且外部内容先存了下来。
	res, err = revert(t, h, RevertOptions{Force: true})
	if err != nil || !res.Applied {
		t.Fatalf("强制还原：%v %+v", err, res)
	}
	h.mustBe("f.txt", "a")
	h.mustBe("g.txt", "g0")
	h.gone("n.txt")
	if b, _ := h.e.Content(sum("external")); string(b) != "external" {
		t.Fatal("覆盖前应先存当前内容")
	}
	// Undo（rewind 截断失败时）：写回还原前。
	if err := h.e.Undo(context.Background(), "s1", res); err != nil {
		t.Fatal(err)
	}
	h.mustBe("f.txt", "external")
	h.mustBe("g.txt", "g1")
	h.mustBe("n.txt", "new")
}

func TestRevertOtherSessionAndDrift(t *testing.T) {
	h := newHarness(t)
	h.put("f.txt", "a")
	h.write("s1", "f.txt", "b")
	h.write("s2", "f.txt", "c")
	res, err := revert(t, h, RevertOptions{})
	if !errors.Is(err, ErrConflict) || res.Conflicts[0].Kind != ConflictOtherSession || res.Conflicts[0].Session != "s2" {
		t.Fatalf("应报会话 s2 改过：%v %+v", err, res)
	}
	h.mustBe("f.txt", "c")

	h2 := newHarness(t)
	h2.put("f.txt", "a")
	h2.write("s1", "f.txt", "b")
	h2.put("f.txt", "hand")
	h2.write("s1", "f.txt", "y")
	res, err = revert(t, h2, RevertOptions{})
	if !errors.Is(err, ErrConflict) || res.Conflicts[0].Kind != ConflictDrift {
		t.Fatalf("范围内有 drift 应算冲突：%v %+v", err, res)
	}
	h2.mustBe("f.txt", "y")
}

func TestRevertUnrevertibleNotForceable(t *testing.T) {
	h := newHarness(t)
	h.e.MaxFileBytes = 4
	h.put("big.txt", "0123456789")
	h.write("s1", "big.txt", "x")
	res, err := revert(t, h, RevertOptions{Force: true})
	if !errors.Is(err, ErrConflict) || res.Conflicts[0].Kind != ConflictUnrevertible || res.Conflicts[0].Forceable {
		t.Fatalf("不可还原的记录强制也不写：%v %+v", err, res)
	}
	h.mustBe("big.txt", "x")
}

func TestRevertLiveFilter(t *testing.T) {
	h := newHarness(t)
	h.put("f.txt", "a")
	h.write("s1", "f.txt", "b") // c1
	res, err := revert(t, h, RevertOptions{Live: func(string) bool { return false }})
	if err != nil || len(res.Files) != 0 {
		t.Fatalf("已回退的调用不参与还原：%v %+v", err, res)
	}
	h.mustBe("f.txt", "b")
}

// tamper 改写本会话唯一一条记录的字段。
func tamper(t *testing.T, h *harness, f func(m map[string]any)) {
	t.Helper()
	ctx := context.Background()
	ds, _, err := h.e.Coll.Query(ctx, store.Tenant, "session", "s1", store.Page{})
	if err != nil || len(ds) != 1 {
		t.Fatalf("取记录：%v %d", err, len(ds))
	}
	f(ds[0].Body)
	if err := h.e.Coll.Put(ctx, store.Tenant, store.Doc{Key: ds[0].Key, Body: ds[0].Body}, ds[0].Version); err != nil {
		t.Fatal(err)
	}
}

func TestRevertRejectsTamperedPaths(t *testing.T) {
	outside := t.TempDir()
	evil := filepath.ToSlash(filepath.Join(outside, "evil.txt"))
	cases := map[string]func(m map[string]any){
		"dotdot":    func(m map[string]any) { m["path"], m["path_key"] = "../evil.txt", PathKey("../evil.txt") },
		"nested":    func(m map[string]any) { m["path"], m["path_key"] = "a/../../evil.txt", PathKey("a/../../evil.txt") },
		"absolute":  func(m map[string]any) { m["path"], m["path_key"] = evil, PathKey(evil) },
		"backslash": func(m map[string]any) { m["path"], m["path_key"] = `..\evil.txt`, PathKey(`..\evil.txt`) },
		"keymismatch": func(m map[string]any) {
			m["path_key"] = "other.txt"
		},
		"otherroot": func(m map[string]any) { m["root"] = filepath.ToSlash(outside) },
	}
	for name, f := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.write("s1", "f.txt", "b")
			tamper(t, h, f)
			before := snapshotDir(t, h.root)
			res, err := revert(t, h, RevertOptions{Force: true})
			if !errors.Is(err, ErrConflict) || len(res.Conflicts) != 1 || res.Conflicts[0].Forceable {
				t.Fatalf("篡改的记录应拒绝且不可强制：%v %+v", err, res)
			}
			if _, err := os.Stat(filepath.Join(outside, "evil.txt")); !os.IsNotExist(err) {
				t.Fatal("不应写到工作区外")
			}
			if !sameSnap(before, snapshotDir(t, h.root)) {
				t.Fatal("不应写盘")
			}
		})
	}
}

// 记录之后目录被换成指向工作区外的符号链接（Windows 上是 junction）：拒绝。
func TestRevertRejectsLinkEscape(t *testing.T) {
	h := newHarness(t)
	outside := t.TempDir()
	h.write("s1", "sub/a.txt", "inside")
	if err := os.RemoveAll(h.abs("sub")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "a.txt"), []byte("inside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := link(outside, h.abs("sub")); err != nil {
		t.Skipf("建不了链接：%v", err)
	}
	res, err := revert(t, h, RevertOptions{Force: true})
	if !errors.Is(err, ErrConflict) || res.Conflicts[0].Kind != ConflictUnsafe {
		t.Fatalf("经链接越界应拒绝：%v %+v", err, res)
	}
	if b, _ := os.ReadFile(filepath.Join(outside, "a.txt")); string(b) != "inside" {
		t.Fatal("工作区外的文件不应被动")
	}
}

// 目标变成了目录：拒绝。
func TestRevertRejectsDirTarget(t *testing.T) {
	h := newHarness(t)
	h.put("f.txt", "a")
	h.write("s1", "f.txt", "b")
	_ = os.Remove(h.abs("f.txt"))
	if err := os.Mkdir(h.abs("f.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := revert(t, h, RevertOptions{Force: true}); !errors.Is(err, ErrConflict) {
		t.Fatalf("目标是目录应拒绝：%v", err)
	}
}
