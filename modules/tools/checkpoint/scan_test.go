package checkpoint

// T9（shell / MCP / 扩展进程改动发现）与 T10（git 未提交改动范围）的单测。git 用例在找不到 git 时跳过。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/modules/internal/wsfs"
	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

// shellAs 以 may_write_files 工具（shell）身份走一次中间件；run 是工具本身。
func (h *harness) shellAs(sid, callID string, run func() toolhub.Output) toolhub.Output {
	h.t.Helper()
	req := component.ToolCallReq{Tool: "shell", Args: json.RawMessage(`{"command":"x"}`), SessionID: sid,
		Risk: toolhub.Dangerous, CallID: callID, Attrs: map[string]any{toolhub.AttrMayWrite: true}, Scope: h.scope}
	resp, err := h.e.Middleware()(context.Background(), req,
		func(context.Context, component.ToolCallReq) (component.ToolCallResp, error) {
			return component.ToolCallResp{Output: run()}, nil
		})
	if err != nil {
		h.t.Fatal(err)
	}
	return resp.Output
}

func (h *harness) shell(sid string, run func()) {
	h.t.Helper()
	h.n++
	h.shellAs(sid, "s"+itoa(h.n), func() toolhub.Output { run(); return toolhub.Text("ok") })
}

// jobOutput job_output（Read 风险）：只标记回合末补扫。
func (h *harness) jobOutput(sid string) {
	h.t.Helper()
	req := component.ToolCallReq{Tool: "job_output", Args: json.RawMessage(`{"id":"job-1"}`), SessionID: sid,
		Risk: toolhub.Read, CallID: "j" + itoa(h.n), Scope: h.scope}
	if _, err := h.e.Middleware()(context.Background(), req,
		func(context.Context, component.ToolCallReq) (component.ToolCallResp, error) {
			return component.ToolCallResp{Output: toolhub.Text("ok")}, nil
		}); err != nil {
		h.t.Fatal(err)
	}
}

func byPath(cs []Change) map[string]Change {
	m := map[string]Change{}
	for _, c := range cs {
		m[c.Path] = c
	}
	return m
}

func needGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("没有 git")
	}
}

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "core.autocrlf=false",
		"-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// 非 git 工作区：新建可还原（= 删除）；之前没记录过的文件改了 / 删了不可还原且说明原因；
// 本会话 write_file 记过的文件被 shell 改了，before 用上一条的 after，可还原并逐字节复原。
func TestShellScanNonGit(t *testing.T) {
	h := newHarness(t)
	h.e.NoGit = true
	h.put("old.txt", "old\n")
	h.put("del.txt", "bye\n")
	h.write("s1", "mine.txt", "原文\r\n第二行") // CRLF、无结尾换行
	h.scope.turn = 2
	h.shell("s1", func() {
		h.put("new.txt", "hello\n")
		h.put("old.txt", "changed\n")
		_ = os.Remove(h.abs("del.txt"))
		h.put("mine.txt", "hi\n")
	})
	m := byPath(h.list("s1"))
	if c := m["new.txt"]; c.Source != SourceShell || c.Before != "" || !c.Revertible || c.Turn != 2 || c.Tool != "shell" {
		t.Fatalf("new.txt：%+v", c)
	}
	for _, p := range []string{"old.txt", "del.txt"} {
		c := m[p]
		if c.Source != SourceShell || c.Revertible || !strings.Contains(c.Why, "不是 git 仓库") || c.Before != unknownHash {
			t.Fatalf("%s 应不可还原并说明原因：%+v", p, c)
		}
	}
	if m["del.txt"].After != "" {
		t.Fatalf("del.txt 删除后 after 应为空：%+v", m["del.txt"])
	}
	if c := m["mine.txt"]; c.Source != SourceShell || !c.Revertible || c.Before != sum("原文\r\n第二行") || !c.BeforeKept {
		t.Fatalf("mine.txt before 应来自上一条记录：%+v", c)
	}
	// diff：本回合（shell 的改动）
	old, _ := h.e.Content(m["mine.txt"].Before)
	cur, _ := h.e.Content(m["mine.txt"].After)
	d := Diff("mine.txt", old, cur)
	if !strings.Contains(d.Unified, "-第二行") || !strings.Contains(d.Unified, "+hi\n") || d.Removed != 2 || d.Added != 1 {
		t.Fatalf("diff：%+v", d)
	}
	// 还原第 2 回合：old.txt / del.txt 不可还原（不可强制）→ 一个字节都不写
	if _, err := revert(t, h, RevertOptions{Session: "s1", Scope: ScopeTurn, Turn: 2, Force: true}); !errors.Is(err, ErrConflict) {
		t.Fatalf("有不可还原的文件应挡住：%v", err)
	}
	h.mustBe("mine.txt", "hi\n")
	// 只还原可还原的两个
	if _, err := revert(t, h, RevertOptions{Session: "s1", Scope: ScopeTurn, Turn: 2, Paths: []string{"mine.txt", "new.txt"}}); err != nil {
		t.Fatal(err)
	}
	h.mustBe("mine.txt", "原文\r\n第二行")
	h.gone("new.txt")
}

// git 工作区：调用前干净的已跟踪文件以 HEAD 为基线（可还原，逐字节复原）；调用前已改的不可还原；
// git checkout -- f 把本会话写过的文件恢复成 HEAD：before 来自上一条记录，可还原。
func TestShellScanGitBaseline(t *testing.T) {
	needGit(t)
	h := newHarness(t)
	gitT(t, h.root, "init", "-q")
	h.put("a.txt", "line1\r\nline2\r\n")
	h.put("b.txt", "bbb\n")
	h.put("dirty.txt", "committed\n")
	h.put("f.txt", "head\n")
	gitT(t, h.root, "add", "-A")
	gitT(t, h.root, "commit", "-q", "-m", "init")
	h.put("dirty.txt", "user edit\n") // 调用前用户已改、未提交
	h.write("s1", "f.txt", "agent\n")
	if !h.e.GitAvailable() {
		t.Fatal("应探测到 git")
	}
	h.scope.turn = 2
	h.shell("s1", func() {
		h.put("a.txt", "line1\r\nCHANGED\r\n")
		_ = os.Remove(h.abs("b.txt"))
		h.put("dirty.txt", "shell\n")
		gitT(t, h.root, "checkout", "--", "f.txt")
	})
	m := byPath(h.list("s1"))
	if c := m["a.txt"]; !c.Revertible || c.Before != sum("line1\r\nline2\r\n") || !c.BeforeKept {
		t.Fatalf("a.txt 应以 HEAD 为基线：%+v", c)
	}
	if c := m["b.txt"]; !c.Revertible || c.After != "" || c.Before != sum("bbb\n") {
		t.Fatalf("b.txt（rm）：%+v", c)
	}
	if c := m["dirty.txt"]; c.Revertible || !strings.Contains(c.Why, "git 里未改动") {
		t.Fatalf("dirty.txt 调用前已改，应不可还原：%+v", c)
	}
	if c := m["f.txt"]; c.Source != SourceShell || !c.Revertible || c.Before != sum("agent\n") || c.After != sum("head\n") {
		t.Fatalf("f.txt（git checkout）：%+v", c)
	}
	if _, err := revert(t, h, RevertOptions{Session: "s1", Scope: ScopeTurn, Turn: 2, Paths: []string{"a.txt", "b.txt", "f.txt"}}); err != nil {
		t.Fatal(err)
	}
	h.mustBe("a.txt", "line1\r\nline2\r\n")
	h.mustBe("b.txt", "bbb\n")
	h.mustBe("f.txt", "agent\n")
}

// 后台 job：调用返回后才写的文件在回合末补扫时记进该回合；没有 may_write / job_* 调用的回合不补扫。
func TestShellBackgroundRescan(t *testing.T) {
	h := newHarness(t)
	h.e.NoGit = true
	h.shell("s1", func() {}) // background:true，立即返回
	h.put("late.txt", "from job\n")
	h.e.AfterTurn("s1", session.EndReason{})
	cs := h.list("s1")
	if len(cs) != 1 || cs[0].Path != "late.txt" || cs[0].Source != SourceShell || cs[0].CallID != "turn-1/scan" || cs[0].Turn != 1 {
		t.Fatalf("回合末应补记 late.txt：%+v", cs)
	}
	// 第 2 回合只调了 job_output：照样补扫，记进第 2 回合
	h.scope.turn = 2
	h.jobOutput("s1")
	h.put("late.txt", "more\n")
	h.e.AfterTurn("s1", session.EndReason{})
	cs = h.list("s1")
	if len(cs) != 2 || cs[1].CallID != "turn-2/scan" || cs[1].Before != sum("from job\n") || !cs[1].Revertible {
		t.Fatalf("job_output 回合应补扫：%+v", cs)
	}
	// 第 3 回合什么工具都没调：不补扫
	h.scope.turn = 3
	h.put("late.txt", "user\n")
	h.e.AfterTurn("s1", session.EndReason{})
	if cs := h.list("s1"); len(cs) != 2 { // 补扫不跑；late.txt 本回合没被碰过，Reconcile 也不补 drift
		t.Fatalf("没有 shell 调用的回合不应补扫：%+v", cs)
	}
	// write_file 写过的文件不会被补扫重复记成 shell
	h.scope.turn = 4
	h.shell("s1", func() {})
	h.write("s1", "w.txt", "x")
	h.e.AfterTurn("s1", session.EndReason{})
	for _, c := range h.list("s1") {
		if c.Path == "w.txt" && c.Source != SourceFS {
			t.Fatalf("w.txt 不应被补扫重复记：%+v", c)
		}
	}
}

// 忽略的目录、.gitignore、Confirm、有基线且内容没变（只碰了 mtime）都不记。
// （没有基线的文件只碰了 mtime 时分不出内容变没变，会记成一条不可还原的改动，见 scan.go。）
func TestShellScanSkips(t *testing.T) {
	h := newHarness(t)
	h.e.NoGit = true
	h.put(".gitignore", "*.log\n")
	h.write("s1", "touch.txt", "same\n") // 有记录 → 有基线
	h.shell("s1", func() {
		for _, p := range []string{"node_modules/x.js", ".venv/y.py", "dist/z.js", ".git/HEAD", "target/t", "a.log"} {
			h.put(p, "x")
		}
		now := time.Now().Add(time.Hour)
		_ = os.Chtimes(h.abs("touch.txt"), now, now)
	})
	if cs := h.list("s1"); len(cs) != 1 || cs[0].Source != SourceFS {
		t.Fatalf("只应有 write_file 那一条：%+v", cs)
	}
	h.shellAs("s1", "c9", func() toolhub.Output {
		h.put("confirm.txt", "x")
		return toolhub.Output{Kind: toolhub.Confirm, Reason: "?"}
	})
	if cs := h.list("s1"); len(cs) != 1 {
		t.Fatalf("Confirm 不应记：%+v", cs)
	}
}

// 超过 scan_max_files：不扫、不报错，ScanSkipped 给出说明，只提示一次。
func TestShellScanTooManyFiles(t *testing.T) {
	h := newHarness(t)
	h.e.NoGit = true
	h.e.ScanMaxFiles = 3
	var notes []string
	h.e.Notify = func(_, msg string) { notes = append(notes, msg) }
	for i := range 4 {
		h.put(fmt.Sprintf("f%d.txt", i), "x")
	}
	if h.e.ScanSkipped("s1") != "" {
		t.Fatal("还没扫过不应标注")
	}
	out := h.shellAs("s1", "c1", func() toolhub.Output { h.put("new.txt", "x"); return toolhub.Text("ok") })
	if out.Kind == toolhub.Confirm || len(h.list("s1")) != 0 {
		t.Fatalf("超上限不应记：%+v", h.list("s1"))
	}
	h.shell("s1", func() {})
	if s := h.e.ScanSkipped("s1"); !strings.Contains(s, "工作区文件太多") || !strings.Contains(s, "3") || len(notes) != 1 {
		t.Fatalf("应标注且只提示一次：%q %v", s, notes)
	}
	if h.e.ScanSkipped("s2") != "" {
		t.Fatal("别的会话不应标注")
	}
	if err := h.e.DeleteSession(context.Background(), "s1"); err != nil || h.e.ScanSkipped("s1") != "" {
		t.Fatalf("删会话应清掉标注：%v", err)
	}
}

// rewind 截掉的 shell 调用视为已回退；回合末补扫的记录不经 Live 判断。
func TestShellLiveFilter(t *testing.T) {
	h := newHarness(t)
	h.e.NoGit = true
	h.shell("s1", func() { h.put("a.txt", "x") })
	cs := h.list("s1")
	if len(cs) != 1 || !cs[0].ByCall() {
		t.Fatalf("%+v", cs)
	}
	if (Change{Source: SourceShell, CallID: "turn-1/scan"}).ByCall() || (Change{Source: SourceDrift}).ByCall() {
		t.Fatal("补扫 / drift 记录不属于调用")
	}
	res, err := revert(t, h, RevertOptions{Session: "s1", Live: func(string) bool { return false }})
	if err != nil || len(res.Files) != 0 {
		t.Fatalf("已回退的调用不应还原：%+v %v", res, err)
	}
}

// —— T10：git 范围 ——//

// 子目录工作区：修改、删除、未跟踪、暂存的新文件、中文和空格文件名；工作区外的改动不出现。
func TestGitList(t *testing.T) {
	needGit(t)
	repo := t.TempDir()
	ws := filepath.Join(repo, "ws")
	w := func(rel, s string) {
		t.Helper()
		p := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitT(t, repo, "init", "-q")
	w("other.txt", "o\n")
	w("ws/mod.txt", "old\nkeep\n")
	w("ws/del.txt", "d\n")
	w("ws/中文 空格.txt", "一\n")
	w("ws/sub/same.txt", "s\n")
	w("ws/bin.dat", "a\x00b")
	gitT(t, repo, "add", "-A")
	gitT(t, repo, "commit", "-q", "-m", "init")
	w("other.txt", "changed\n")
	w("ws/mod.txt", "new\nkeep\n")
	_ = os.Remove(filepath.Join(ws, "del.txt"))
	w("ws/中文 空格.txt", "二\n三\n")
	w("ws/sub/new file.txt", "n1\nn2\n")
	w("ws/staged.txt", "st\n")
	w("ws/bin.dat", "a\x00c")
	gitT(t, ws, "add", "staged.txt")

	e := New(ws, nil, nil)
	if !e.GitAvailable() {
		t.Fatal("应探测到 git")
	}
	fs, err := e.GitList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range fs {
		got = append(got, fmt.Sprintf("%s:%s:+%d-%d:u=%v:b=%v", f.Path, f.Status, f.Added, f.Removed, f.Untracked, f.Binary))
	}
	want := []string{
		"bin.dat:modified:+0-0:u=false:b=true",
		"del.txt:deleted:+0-1:u=false:b=false",
		"mod.txt:modified:+1-1:u=false:b=false",
		"staged.txt:added:+1-0:u=false:b=false",
		"sub/new file.txt:added:+2-0:u=true:b=false",
		"中文 空格.txt:modified:+2-1:u=false:b=false",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("GitList:\n%s\n期望:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	d, err := e.GitDiff(context.Background(), "mod.txt")
	if err != nil || !strings.HasPrefix(d.Unified, "--- a/mod.txt\n+++ b/mod.txt\n@@") || !strings.Contains(d.Unified, "-old\n+new\n") || d.OldSize != 9 || d.NewSize != 9 {
		t.Fatalf("diff mod：%+v %v", d, err)
	}
	if d, err := e.GitDiff(context.Background(), "del.txt"); err != nil || !strings.Contains(d.Unified, "+++ /dev/null") {
		t.Fatalf("diff del：%+v %v", d, err)
	}
	if d, err := e.GitDiff(context.Background(), "sub/new file.txt"); err != nil || !strings.HasPrefix(d.Unified, "--- /dev/null\n+++ b/sub/new file.txt\n") {
		t.Fatalf("diff 未跟踪：%+v %v", d, err)
	}
	if d, err := e.GitDiff(context.Background(), "中文 空格.txt"); err != nil || !strings.Contains(d.Unified, "+三") {
		t.Fatalf("diff 中文：%+v %v", d, err)
	}
	if d, err := e.GitDiff(context.Background(), "bin.dat"); err != nil || !d.Binary || d.Unified != "" {
		t.Fatalf("diff 二进制：%+v %v", d, err)
	}
	for _, p := range []string{"sub/same.txt", "../other.txt", "nope.txt"} {
		if _, err := e.GitDiff(context.Background(), p); !errors.Is(err, ErrNotInGit) {
			t.Fatalf("%s 应 ErrNotInGit：%v", p, err)
		}
	}
}

// 还没有提交的仓库：与空树比；暂存与未跟踪的都算新增。
func TestGitListNoCommits(t *testing.T) {
	needGit(t)
	dir := t.TempDir()
	gitT(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, dir, "add", "a.txt")
	e := New(dir, nil, nil)
	fs, err := e.GitList(context.Background())
	if err != nil || len(fs) != 2 || fs[0].Status != "added" || fs[0].Added != 1 || fs[1].Status != "added" || !fs[1].Untracked {
		t.Fatalf("%+v %v", fs, err)
	}
	if d, err := e.GitDiff(context.Background(), "a.txt"); err != nil || !strings.Contains(d.Unified, "+1") {
		t.Fatalf("%+v %v", d, err)
	}
}

// 不是 git 仓库、或模拟没有 git：GitAvailable 为假，GitList 报错。
func TestGitUnavailable(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "rev-parse", "--is-inside-work-tree").Output(); err == nil && strings.TrimSpace(string(out)) == "true" {
		t.Skip("临时目录在某个 git 仓库里")
	}
	e := New(dir, nil, nil)
	if e.GitAvailable() {
		t.Fatal("非仓库不应报 git")
	}
	if _, err := e.GitList(context.Background()); err == nil {
		t.Fatal("应报错")
	}
	e2 := New(dir, nil, nil)
	e2.NoGit = true
	if e2.GitAvailable() {
		t.Fatal("NoGit")
	}
}

// —— 扫描耗时（T9 完成标准）：TENON_SCAN_BENCH=1 时跑；TENON_SCAN_BENCH_DIR 指定放临时目录的上级目录（缺省 t.TempDir）。
// 生成 2 万个文件（200 个目录 × 100 个），量冷 / 热两次 Walk、一次 git status，以及一次完整的 shell 调用开销。
func TestScanTiming20k(t *testing.T) {
	if os.Getenv("TENON_SCAN_BENCH") == "" {
		t.Skip("设 TENON_SCAN_BENCH=1 才跑")
	}
	base := os.Getenv("TENON_SCAN_BENCH_DIR")
	if base == "" {
		base = t.TempDir()
	}
	root, err := os.MkdirTemp(base, "scan20k-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	start := time.Now()
	for d := range 200 {
		dir := filepath.Join(root, fmt.Sprintf("d%03d", d))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for f := range 100 {
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%03d.txt", f)), []byte("x\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Logf("生成 20000 个文件：%v（%s）", time.Since(start), root)
	// 尽量清页缓存（需要免密 sudo；不行就是「新进程第一次遍历」）
	cold := "新进程第一次遍历（未清页缓存）"
	if exec.Command("sudo", "-n", "sh", "-c", "sync; echo 3 > /proc/sys/vm/drop_caches").Run() == nil {
		cold = "清页缓存后"
	}
	e := New(root, nil, nil)
	e.NoGit = true
	for i, label := range []string{"冷：" + cold, "热"} {
		t0 := time.Now()
		m, err := wsfs.WalkSkip(context.Background(), root, DefaultScanMaxFiles, e.skipDirs())
		if err != nil || len(m) != 20000 {
			t.Fatalf("第 %d 次：%d %v", i, len(m), err)
		}
		t.Logf("Walk 2 万文件 %s：%v", label, time.Since(t0))
	}
	h := &harness{t: t, e: e, art: &fakeArt{}, root: root, scope: &fakeScope{turn: 1}}
	t0 := time.Now()
	h.shell("s1", func() {})
	t.Logf("一次 shell 调用的扫描开销（两次 Walk，无 git）：%v", time.Since(t0))
	if _, err := exec.LookPath("git"); err == nil {
		gitT(t, root, "init", "-q")
		gitT(t, root, "add", "-A")
		gitT(t, root, "commit", "-q", "-m", "x")
		e2 := New(root, nil, nil)
		t0 = time.Now()
		if _, err := e2.gitStatus(context.Background(), false); err != nil {
			t.Fatal(err)
		}
		t.Logf("git status（2 万个已跟踪文件）：%v", time.Since(t0))
		h2 := &harness{t: t, e: e2, art: &fakeArt{}, root: root, scope: &fakeScope{turn: 1}}
		t0 = time.Now()
		h2.shell("s1", func() {})
		t.Logf("一次 shell 调用的扫描开销（两次 Walk + git status）：%v", time.Since(t0))
	}
}
