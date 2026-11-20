package apphost

// M11 验收（docs/50 5.1，配置T14）：MCP 经统一资源管理器分配实例，走真实的 OpenWorkspaceManager。
// 假 stdio MCP 服务就是本测试二进制自己（TestMain 见 FAKE_MCP=1 时当服务跑），每次启动往 FAKE_PID_FILE 追加一行 pid。

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/modules/tools/mcp"
	"github.com/Sevyn17/tenon/runtime/assemble"
)

func TestMain(m *testing.M) {
	if os.Getenv("FAKE_MCP") == "1" {
		fakeMCPServe()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fakeMCPServe 最小的 stdio MCP 服务：initialize、tools/list（一个工具，名字取 FAKE_TOOL）。
func fakeMCPServe() {
	if p := os.Getenv("FAKE_PID_FILE"); p != "" {
		f, _ := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		fmt.Fprintf(f, "%d\n", os.Getpid())
		f.Close()
	}
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var m struct {
			ID     *json.RawMessage `json:"id"`
			Method string           `json:"method"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil || m.ID == nil {
			continue
		}
		var res any
		switch m.Method {
		case "initialize":
			res = map[string]any{"protocolVersion": mcp.ProtocolVersion, "serverInfo": map[string]any{"name": "fake"}, "capabilities": map[string]any{}}
		case "tools/list":
			res = map[string]any{"tools": []any{map[string]any{"name": os.Getenv("FAKE_TOOL"), "description": "假工具", "inputSchema": map[string]any{"type": "object"}}}}
		default:
			res = map[string]any{}
		}
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": res})
		os.Stdout.Write(append(b, '\n'))
	}
}

// fakeServerTOML 一个 [[server]] 段：命令是本测试二进制，env 让它当假服务跑。
func fakeServerTOML(t *testing.T, name, scope, pidFile string) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	s := fmt.Sprintf("[[server]]\nname = %q\ncommand = '%s'\ntimeout = 10\n", name, exe)
	if scope != "" {
		s += fmt.Sprintf("scope = %q\n", scope)
	}
	return s + fmt.Sprintf("env = { FAKE_MCP = \"1\", FAKE_TOOL = \"%s_tool\", FAKE_PID_FILE = '%s' }\n\n", name, pidFile)
}

func starts(t *testing.T, pidFile string) int {
	t.Helper()
	b, err := os.ReadFile(pidFile)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(b), "\n")
}

// mcpTools 当前工作区会话能看到的 MCP 工具名。
func mcpTools(t *testing.T, wm *WorkspaceManager) []string {
	t.Helper()
	c, err := assemble.Collect(wm.Current().Runtime.App.Internals().Assembly)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, ts := range c.Tools {
		for _, tl := range ts.Tools() {
			if strings.HasPrefix(tl.Spec.Name, "mcp__") {
				out = append(out, tl.Spec.Name)
			}
		}
	}
	slices.Sort(out)
	return out
}

func mcpService(t *testing.T, wm *WorkspaceManager) *mcp.ServiceAPI {
	t.Helper()
	v, ok := wm.Current().Runtime.App.Internals().Assembly.Value(mcp.Service)
	if !ok {
		t.Fatal("没有 service:mcp")
	}
	return v.(*mcp.ServiceAPI)
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	dl := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(dl) {
			t.Fatalf("等不到：%s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

type mcpEnv struct {
	home, a, b, pids string
	profile          string
}

// newMCPEnv 用户级目录（TENON_HOME）+ 两个工作区目录 + 打开 mcp 插件的 profile；userTOML 写进 <home>/mcp.toml。
func newMCPEnv(t *testing.T, userTOML string) *mcpEnv {
	t.Helper()
	e := &mcpEnv{home: t.TempDir(), a: t.TempDir(), b: t.TempDir()}
	t.Setenv("TENON_HOME", e.home)
	t.Setenv("TENON_BASE_URL", "")
	t.Setenv("TENON_MODEL", "")
	e.pids = filepath.Join(t.TempDir(), "pids")
	e.profile = filepath.Join(t.TempDir(), "mcp-profile.toml")
	if err := os.WriteFile(e.profile, []byte("[[plugin]]\nname = \"mcp\"\nenabled = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if userTOML != "" {
		if err := os.WriteFile(filepath.Join(e.home, "mcp.toml"), []byte(userTOML), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func (e *mcpEnv) open(t *testing.T, idle time.Duration) *WorkspaceManager {
	t.Helper()
	wm, err := OpenWorkspaceManager(WorkspaceOptions{Flags: Flags{Workdir: e.a, Profile: e.profile, Yes: true, MaxSteps: -1},
		RecentPath: filepath.Join(e.home, "recent.json"), ResourceIdle: idle})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(wm.Close)
	return wm
}

func instancesOf(wm *WorkspaceManager, id string) (n, refs int) {
	for _, in := range wm.Resources().List() {
		if in.Key.Kind == mcp.ResourceKind && in.Name == id {
			n++
			refs += in.Refs
		}
	}
	return
}

// 切换工作区、重建运行时：全局范围的服务进程不重启（启动次数不变、同一个实例）；工作区范围的按工作区各起一份。
func TestMCPGlobalSurvivesSwitch(t *testing.T) {
	pidsG := filepath.Join(t.TempDir(), "g")
	pidsW := filepath.Join(t.TempDir(), "w")
	e := newMCPEnv(t, "")
	if err := os.WriteFile(filepath.Join(e.home, "mcp.toml"),
		[]byte(fakeServerTOML(t, "g", "global", pidsG)+fakeServerTOML(t, "w", "", pidsW)), 0o644); err != nil {
		t.Fatal(err)
	}
	wm := e.open(t, time.Hour)
	if got := mcpTools(t, wm); !slices.Equal(got, []string{"mcp__g__g_tool", "mcp__w__w_tool"}) {
		t.Fatalf("A 的工具：%v", got)
	}
	gKey := mcpService(t, wm).Servers()[0].Instance
	if _, err := wm.Open(context.Background(), e.b); err != nil {
		t.Fatal(err)
	}
	if _, err := wm.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if g, w := starts(t, pidsG), starts(t, pidsW); g != 1 || w != 2 {
		t.Fatalf("切换 + 重建后：全局服务启动 %d 次（应 1），工作区服务启动 %d 次（应 2：A、B 各一份）", g, w)
	}
	if got := mcpTools(t, wm); !slices.Equal(got, []string{"mcp__g__g_tool", "mcp__w__w_tool"}) {
		t.Fatalf("B 的工具：%v", got)
	}
	st := mcpService(t, wm).Servers()
	if st[0].Instance != gKey || st[0].Scope != mcp.ScopeGlobal || st[0].State != mcp.StateConnected || st[0].Source != mcp.SourceUser {
		t.Fatalf("全局服务应是同一个实例：%+v vs %+v", st[0].Instance, gKey)
	}
	if st[1].Instance.Workspace != WorkspaceKey(e.b) || st[1].Cwd != e.b {
		t.Fatalf("工作区服务应按 B 分配、工作目录为 B：%+v", st[1])
	}
	if n, refs := instancesOf(wm, "g"); n != 1 || refs != 1 {
		t.Fatalf("全局实例 %d 份、引用 %d（应 1、1）", n, refs)
	}
	if n, refs := instancesOf(wm, "w"); n != 2 || refs != 1 { // A 的那份空闲中（1 小时后回收）
		t.Fatalf("工作区实例 %d 份、引用 %d（应 2、1）", n, refs)
	}
	if got := mcpService(t, wm).Instances(); len(got) != 3 {
		t.Fatalf("service:mcp 的实例列表：%+v", got)
	}
	wm.Close()
	if got := wm.Resources().List(); len(got) != 0 {
		t.Fatalf("关闭后不应还有实例：%+v", got)
	}
}

// 同一服务在 A 工作区启用、B 工作区不启用：两边工具列表不同；B 的状态里标明停用。
func TestMCPEnabledPerWorkspace(t *testing.T) {
	pids := filepath.Join(t.TempDir(), "g")
	e := newMCPEnv(t, "")
	if err := os.WriteFile(filepath.Join(e.home, "mcp.toml"), []byte(fakeServerTOML(t, "g", "global", pids)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := (&mcp.Enabled{Workspace: mcp.WorkspaceKey(e.b)}).Set("g", false, mcp.EnableWorkspace); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.home, mcp.EnabledFile)); err != nil {
		t.Fatalf("启用记录应写在用户级目录：%v", err)
	}
	wm := e.open(t, time.Hour)
	if got := mcpTools(t, wm); !slices.Equal(got, []string{"mcp__g__g_tool"}) {
		t.Fatalf("A 应有 g 的工具：%v", got)
	}
	if _, err := wm.Open(context.Background(), e.b); err != nil {
		t.Fatal(err)
	}
	if got := mcpTools(t, wm); len(got) != 0 {
		t.Fatalf("B 没启用 g，不应有它的工具：%v", got)
	}
	st := mcpService(t, wm).Servers()
	if len(st) != 1 || st[0].Enabled || st[0].DisabledScope != mcp.EnableWorkspace || st[0].State != mcp.StateDisabled {
		t.Fatalf("B 里 g 应列出且标停用：%+v", st)
	}
	// B 没有引用：全局实例空闲中（A 关了、B 不用），不重启也不报错。
	if n, refs := instancesOf(wm, "g"); n != 1 || refs != 0 {
		t.Fatalf("g 实例 %d 份、引用 %d（应 1、0）", n, refs)
	}
}

// 没有引用的实例按空闲时长回收；全局实例在切换的空档里不被回收（空闲时长很短也一样）。
func TestMCPIdleReclaim(t *testing.T) {
	pidsG := filepath.Join(t.TempDir(), "g")
	pidsW := filepath.Join(t.TempDir(), "w")
	e := newMCPEnv(t, "")
	if err := os.WriteFile(filepath.Join(e.home, "mcp.toml"),
		[]byte(fakeServerTOML(t, "g", "global", pidsG)+fakeServerTOML(t, "w", "", pidsW)), 0o644); err != nil {
		t.Fatal(err)
	}
	wm := e.open(t, 150*time.Millisecond)
	if _, err := wm.Open(context.Background(), e.b); err != nil {
		t.Fatal(err)
	}
	keyA := WorkspaceKey(e.a)
	waitUntil(t, "A 的工作区实例回收", func() bool {
		for _, in := range wm.Resources().List() {
			if in.Key.Workspace == keyA {
				return false
			}
		}
		return true
	})
	if g := starts(t, pidsG); g != 1 {
		t.Fatalf("全局服务启动了 %d 次，切换的空档里不应被回收", g)
	}
	if n, refs := instancesOf(wm, "w"); n != 1 || refs != 1 {
		t.Fatalf("应只剩 B 的工作区实例：%d 份、引用 %d", n, refs)
	}
	// 停用后重建：g 没人引用了，按空闲时长回收。
	if err := (&mcp.Enabled{Workspace: mcp.WorkspaceKey(e.b)}).Set("g", false, mcp.EnableWorkspace); err != nil {
		t.Fatal(err)
	}
	if _, err := wm.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "全局实例没有引用后回收", func() bool { n, _ := instancesOf(wm, "g"); return n == 0 })
}

// 未受信项目的 <数据目录>/mcp.toml 不加载（不起进程、不列出）；受信后作为该工作区的定义生效。
func TestMCPUntrustedProjectNotLoaded(t *testing.T) {
	pids := filepath.Join(t.TempDir(), "p")
	e := newMCPEnv(t, "")
	for _, dir := range []string{e.a, e.b} {
		pd := filepath.Join(dir, ".tenon")
		if err := os.MkdirAll(pd, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pd, "mcp.toml"), []byte(fakeServerTOML(t, "p", "global", pids)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := SaveTrust(ProjectKey(e.b), Trusted); err != nil {
		t.Fatal(err)
	}
	wm := e.open(t, time.Hour)
	if wm.Current().Runtime.Trusted {
		t.Fatal("A 不应受信")
	}
	if got := mcpTools(t, wm); len(got) != 0 {
		t.Fatalf("未受信项目的 mcp.toml 不应加载：%v", got)
	}
	if st := mcpService(t, wm).Servers(); len(st) != 0 {
		t.Fatalf("未受信项目的服务不应列出：%+v", st)
	}
	if n := starts(t, pids); n != 0 {
		t.Fatalf("未受信项目的服务不应启动，启动了 %d 次", n)
	}
	if _, err := wm.Open(context.Background(), e.b); err != nil {
		t.Fatal(err)
	}
	if got := mcpTools(t, wm); !slices.Equal(got, []string{"mcp__p__p_tool"}) {
		t.Fatalf("受信项目的服务应生效：%v", got)
	}
	st := mcpService(t, wm).Servers()
	if len(st) != 1 || st[0].Source != mcp.SourceProject || st[0].Scope != mcp.ScopeWorkspace ||
		st[0].Instance.Workspace != WorkspaceKey(e.b) {
		t.Fatalf("项目定义应按工作区分配（scope=global 无效）：%+v", st)
	}
}
