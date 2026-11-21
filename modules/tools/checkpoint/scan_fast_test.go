package checkpoint

// 2026-11-21 扫描提速：只读命令判定、增量快照下的外部改动、git status 复用、监视器释放。

import (
	"context"
	"encoding/json"
	"runtime"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/runtime/component"
	"github.com/Sevyn17/tenon/runtime/session"
	"github.com/Sevyn17/tenon/runtime/toolhub"
)

func TestReadOnlyCommand(t *testing.T) {
	yes := []string{
		"ls -la", "cat a.txt", "grep -rn foo .", "rg TODO src", "find . -name '*.go'", "sed -n '1,20p' a.go",
		"sed -n 5p a.go", "git status", "git log --oneline -5", "git diff HEAD~1", "git show HEAD:a.go",
		"git rev-parse HEAD", "git ls-files", "head -n 3 \"a b.txt\"", "wc -l a.go", "sort a.txt", "tree -L 2",
		"pwd",
	}
	for _, c := range []string{"Get-Content a.txt", "Select-String -Path a.go -Pattern foo", "GIT.EXE status"} {
		if !readOnlyCommand(c, true) {
			t.Errorf("Windows 上应判为只读：%q", c)
		}
	}
	for _, c := range []string{"Set-Content a x", "Remove-Item a", "Get-Content a | Set-Content b", "Get-Content $(x)"} {
		if readOnlyCommand(c, true) {
			t.Errorf("Windows 上不应判为只读：%q", c)
		}
	}
	no := []string{
		"", "rm a.txt", "ls; rm a", "ls && touch b", "ls || true", "cat a | sh", "echo x > a.txt", "echo x >> a",
		"cat < a", "echo $(touch x)", "echo `touch x`", "sleep 1 &", "find . -delete", "find . -exec rm {} ;",
		"find . -execdir rm {} +", "find . -fprint out", "sed -i s/a/b/ f", "sed --in-place s/a/b/ f",
		"sed -ni p f", "sed -f s.sed f", "sed 'w out' f", "sed -n '1e touch x' f", "git commit -m x",
		"git checkout -- a", "git -C x status", "git -c core.pager=x log", "git diff --output=x",
		"git log --ext-diff", "git", "sort -o out a", "sort --output=out a", "tree -o out", "rg --pre x foo",
		"./ls", "/bin/ls", "FOO=1 ls", "cat 'unterminated", "python x.py", "npm test", "make",
		"Get-Content a | Set-Content b", "Write-Output @(1)", "echo {a,b}", "ls\nrm a",
	}
	for _, c := range yes {
		if !readOnlyCommand(c, false) {
			t.Errorf("应判为只读：%q", c)
		}
	}
	for _, c := range no {
		if readOnlyCommand(c, false) {
			t.Errorf("不应判为只读：%q", c)
		}
	}
	if !readOnlyCommand("LS.EXE", true) || readOnlyCommand("LS.EXE", false) {
		t.Error("Windows 上命令名不分大小写、可带 .exe；其他平台不行")
	}
	if readOnlyShell("mcp_x", json.RawMessage(`{"command":"ls"}`)) || !readOnlyShell("shell", json.RawMessage(`{"command":"ls"}`)) {
		t.Error("只认 shell 工具")
	}
}

// shellCmd 以 shell 工具身份跑一条命令（只读判定看 command）。
func (h *harness) shellCmd(sid, cmd string, run func()) {
	h.t.Helper()
	h.n++
	args, _ := json.Marshal(map[string]string{"command": cmd})
	req := component.ToolCallReq{Tool: "shell", Args: args, SessionID: sid, Risk: toolhub.Dangerous,
		CallID: "s" + itoa(h.n), Attrs: map[string]any{toolhub.AttrMayWrite: true}, Scope: h.scope}
	if _, err := h.e.Middleware()(context.Background(), req,
		func(context.Context, component.ToolCallReq) (component.ToolCallResp, error) {
			run()
			return component.ToolCallResp{Output: toolhub.Text("ok")}, nil
		}); err != nil {
		h.t.Fatal(err)
	}
}

// 只读命令前后不扫；它要是真写了文件（判定失误、或被它启动的后台进程写），回合末补扫兜底记下。
func TestReadOnlyShellSkipsScan(t *testing.T) {
	h := newHarness(t)
	h.e.NoGit = true
	defer h.e.Close()
	h.put("a.txt", "a\n")
	h.shellCmd("s1", "touch x", func() {}) // 建立快照
	full := h.e.treeFor().Stats()
	h.shellCmd("s1", "cat a.txt", func() { h.put("sneaky.txt", "x\n") })
	if st := h.e.treeFor().Stats(); st != full {
		t.Fatalf("只读命令不应扫描：%+v → %+v", full, st)
	}
	if cs := h.list("s1"); len(cs) != 0 {
		t.Fatalf("只读命令不应在调用时记：%+v", cs)
	}
	h.e.AfterTurn("s1", session.EndReason{})
	cs := h.list("s1")
	if len(cs) != 1 || cs[0].Path != "sneaky.txt" || cs[0].CallID != "turn-1/scan" {
		t.Fatalf("回合末补扫应兜底记下：%+v", cs)
	}
}

// 两次 shell 调用之间的外部改动（用户在编辑器里改）：增量快照在调用前就看到，不算到 shell 头上；
// 本会话记过的文件之后再被写时补 drift（与原先每次全量遍历的语义一致）。
func TestExternalChangeBetweenCalls(t *testing.T) {
	h := newHarness(t)
	h.e.NoGit = true
	defer h.e.Close()
	h.write("s1", "mine.txt", "v1\n")
	h.put("other.txt", "o\n")
	h.shellCmd("s1", "make", func() {})
	time.Sleep(20 * time.Millisecond) // mtime 与上一条记录拉开
	h.put("mine.txt", "user edit\n")  // 外部改动
	h.put("other.txt", "user\n")
	h.put("new.txt", "user new\n")
	h.shellCmd("s1", "make", func() { h.put("built.txt", "b\n") })
	m := byPath(h.list("s1"))
	if c, ok := m["built.txt"]; !ok || c.Source != SourceShell {
		t.Fatalf("shell 自己写的应记：%+v", m)
	}
	for _, p := range []string{"mine.txt", "other.txt", "new.txt"} {
		if c, ok := m[p]; ok && c.Source == SourceShell {
			t.Fatalf("外部改动 %s 不应算到 shell 头上：%+v", p, c)
		}
	}
	h.write("s1", "mine.txt", "v2\n")
	var drift *Change
	for _, c := range h.list("s1") {
		if c.Path == "mine.txt" && c.Source == SourceDrift {
			c := c
			drift = &c
		}
	}
	if drift == nil || drift.Before != sum("v1\n") || drift.After != sum("user edit\n") {
		t.Fatalf("外部改动应记成 drift：%+v", drift)
	}
	if st := h.e.treeFor().Stats(); h.e.treeFor().Watching() && st.Full != 1 {
		t.Fatalf("有监视器时应只全量一次：%+v", st)
	}
}

// 没有改动时第二次调用复用快照、也不重跑 git status；工作区或 git 目录一变就重跑。
func TestGitStatusReuse(t *testing.T) {
	needGit(t)
	h := newHarness(t)
	defer h.e.Close()
	gitT(t, h.root, "init", "-q")
	h.put("a.txt", "a\n")
	gitT(t, h.root, "add", "-A")
	gitT(t, h.root, "commit", "-q", "-m", "init")
	h.shellCmd("s1", "make", func() {})
	if !h.e.treeFor().Watching() {
		t.Skip("本机文件系统不支持变更通知")
	}
	n0 := h.e.statusRuns.Load()
	h.shellCmd("s1", "make", func() {})
	if n := h.e.statusRuns.Load(); n != n0 {
		t.Fatalf("没有改动不应重跑 git status：%d → %d", n0, n)
	}
	h.put("a.txt", "changed\n") // 外部改动：调用前 git status 必须重跑，a.txt 不能再用 HEAD 当基线
	h.shellCmd("s1", "make", func() { h.put("a.txt", "shell\n") })
	if n := h.e.statusRuns.Load(); n == n0 {
		t.Fatal("工作区变了应重跑 git status")
	}
	if c := byPath(h.list("s1"))["a.txt"]; c.Revertible {
		t.Fatalf("调用前已被外部改过，不应以 HEAD 为基线：%+v", c)
	}
	n1 := h.e.statusRuns.Load()
	gitT(t, h.root, "add", "-A") // 只动 git 目录（index）
	h.shellCmd("s1", "make", func() {})
	if n := h.e.statusRuns.Load(); n == n1 {
		t.Fatal("index 变了应重跑 git status")
	}
}

// 引擎关闭、最后一个会话删除时释放监视器；没有残留 goroutine。
func TestEngineReleasesWatcher(t *testing.T) {
	g0 := runtime.NumGoroutine()
	for range 3 {
		h := newHarness(t)
		h.e.NoGit = true
		h.put("a.txt", "a\n")
		h.shellCmd("s1", "make", func() {})
		tr := h.e.treeFor()
		if err := h.e.DeleteSession(context.Background(), "s1"); err != nil {
			t.Fatal(err)
		}
		if tr.Watching() {
			t.Fatal("最后一个会话删除后应释放监视器")
		}
		h.shellCmd("s2", "make", func() {}) // 懒启动一个新的
		tr = h.e.treeFor()
		if err := h.e.Close(); err != nil || tr.Watching() {
			t.Fatalf("Close 应释放监视器：%v", err)
		}
	}
	time.Sleep(50 * time.Millisecond)
	if g := runtime.NumGoroutine(); g > g0 {
		t.Fatalf("goroutine %d → %d", g0, g)
	}
}
