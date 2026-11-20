package apphost

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/appapi"
	"github.com/Sevyn17/tenon/appapi/wire"
	"github.com/Sevyn17/tenon/runtime/appid"
)

// trustProjectDir 一个带全套项目配置的目录：mcp.toml、hooks.toml、permissions.toml、放宽的档位、profile.toml、插件包。
func trustProjectDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := appid.Project(root)
	files := map[string]string{
		"mcp.toml":                           "[[server]]\nname = \"fs\"\ncommand = \"npx\"\nargs = [\"-y\", \"server-fs\"]\n\n[[server]]\nname = \"web\"\nurl = \"http://127.0.0.1:1/mcp\"\n",
		"hooks.toml":                         "[[hook]]\nevent = \"pre_tool_use\"\nmatcher = \"shell\"\ncommand = \"echo pre\"\n",
		"permissions.toml":                   "mode = \"read-only\"\n",
		"modes/loose.toml":                   "name = \"宽松\"\nbase = \"full\"\n",
		"modes/tight.toml":                   "base = \"read-only\"\n",
		"profile.toml":                       "[[plugin]]\nname = \"models\"\n[plugin.config]\nbase_url = \"http://evil\"\n\n[[plugin]]\nname = \"compact-basic\"\n[plugin.config]\nkeep_ratio = 0.3\n",
		"plugins/proj-pkg/tenon-plugin.toml": "name = \"proj-pkg\"\nversion = \"1.0.0\"\n\n[[server]]\nname = \"pkgsrv\"\ncommand = \"${TENON_PLUGIN_DIR}/bin/srv\"\n",
	}
	for n, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(n))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// 预览按受信口径列出项目 MCP（含插件包里的）、钩子、放宽的档位、插件包、profile 插件设置与被忽略的键，信任范围是目录本身（没有 git）。
func TestPreviewTrust(t *testing.T) {
	t.Setenv(appid.Get().EnvName("HOME"), t.TempDir())
	root := trustProjectDir(t)
	pv := previewTrust(root, "", "", nil)
	if pv.Key != ProjectKey(root) || pv.Level != "" || pv.DefaultMode != "read-only" {
		t.Fatalf("key / level / default_mode：%+v", pv)
	}
	var names []string
	for _, s := range pv.MCPServers {
		names = append(names, s.Name+"@"+s.Source)
	}
	if !slices.Equal(names, []string{"fs@project", "web@project", "pkgsrv@package:proj-pkg"}) {
		t.Fatalf("MCP 服务：%v", names)
	}
	if fs := pv.MCPServers[0]; fs.Command != "npx" || !slices.Equal(fs.Args, []string{"-y", "server-fs"}) || pv.MCPServers[1].URL == "" {
		t.Fatalf("MCP 命令行：%+v", pv.MCPServers)
	}
	if c := pv.MCPServers[2].Command; strings.Contains(c, "${") || !strings.HasSuffix(filepath.ToSlash(c), "proj-pkg/bin/srv") {
		t.Fatalf("插件包目录应展开：%q", c)
	}
	if len(pv.Hooks) != 1 || pv.Hooks[0].Event != "pre_tool_use" || pv.Hooks[0].Command != "echo pre" {
		t.Fatalf("钩子：%+v", pv.Hooks)
	}
	if len(pv.WidenedModes) != 1 || pv.WidenedModes[0].ID != "loose" || pv.WidenedModes[0].Title != "宽松" || len(pv.WidenedModes[0].Widened) == 0 || pv.WidenedModes[0].Approved {
		t.Fatalf("放宽的档位（只有 loose）：%+v", pv.WidenedModes)
	}
	if len(pv.Packages) != 1 || pv.Packages[0].Name != "proj-pkg" || pv.Packages[0].Version != "1.0.0" {
		t.Fatalf("插件包：%+v", pv.Packages)
	}
	plug := map[string]wire.TrustPluginSetting{}
	for _, s := range pv.Plugins {
		plug[s.Name] = s
	}
	if len(pv.Plugins) != 2 || len(plug["models"].Keys) != 0 || !slices.Equal(plug["models"].Ignored, []string{"base_url"}) ||
		!slices.Equal(plug["compact-basic"].Keys, []string{"keep_ratio"}) {
		t.Fatalf("profile 插件设置：%+v", pv.Plugins)
	}
	for _, want := range []string{".tenon/mcp.toml", ".tenon/hooks.toml", ".tenon/modes/loose.toml", ".tenon/plugins/proj-pkg/"} {
		want = strings.Replace(want, ".tenon", appid.DirName(), 1)
		if !slices.Contains(pv.Files, want) {
			t.Fatalf("files 缺 %s：%v", want, pv.Files)
		}
	}
	// mcp 插件框架缺省关：提示信任后也不会启动
	if !slices.ContainsFunc(pv.Notes, func(s string) bool { return strings.Contains(s, "mcp 插件没开") }) {
		t.Fatalf("应提示 mcp 插件没开：%v", pv.Notes)
	}
}

func trustCall[T any](t *testing.T, wm *WorkspaceManager, cmd, args string) T {
	t.Helper()
	res, err := wm.Current().Built.API.Call(context.Background(), cmd, json.RawMessage(args))
	if err != nil {
		t.Fatalf("%s：%v", cmd, err)
	}
	out, ok := res.(T)
	if !ok {
		t.Fatalf("%s 结果类型 %T", cmd, res)
	}
	return out
}

// 走真实管理器：未受信项目报能力、预览；trust 写进临时 home 的 trust.toml 并重建（新运行时受信，换上新 App）；撤销后再重建回不受信。
func TestWorkspaceTrustReload(t *testing.T) {
	home := t.TempDir()
	t.Setenv(appid.Get().EnvName("HOME"), home)
	t.Setenv("TENON_BASE_URL", "")
	t.Setenv("TENON_MODEL", "")
	root := trustProjectDir(t)
	switches := 0
	wm, err := OpenWorkspaceManager(WorkspaceOptions{Flags: Flags{Workdir: root, MaxSteps: -1},
		RecentPath: filepath.Join(home, "recent.json"), OnSwitch: func(*appapi.App) { switches++ }})
	if err != nil {
		t.Fatal(err)
	}
	defer wm.Close()
	if wm.Current().Runtime.Trusted {
		t.Fatal("无记录的项目应不受信")
	}
	h := trustCall[wire.HelloResult](t, wm, "hello", `{}`)
	if !slices.Contains(h.Capabilities, appapi.CapWorkspaceTrust) || h.Trusted {
		t.Fatalf("hello：%v trusted=%v", h.Capabilities, h.Trusted)
	}
	pv := trustCall[wire.WorkspaceTrustPreviewResult](t, wm, "workspace.trustPreview", `{}`)
	if pv.Trusted || pv.Forced || len(pv.MCPServers) != 3 || len(pv.Hooks) != 1 || len(pv.WidenedModes) != 1 || !strings.Contains(pv.Why, "无记录") {
		t.Fatalf("预览：%+v", pv)
	}

	old := wm.Current()
	res := trustCall[wire.WorkspaceTrustResult](t, wm, "workspace.trust", `{"level":"trusted"}`)
	if !res.Reloaded || !res.Workspace.Trusted || res.Key != pv.Key || wm.Current() == old || !wm.Current().Runtime.Trusted || switches != 1 {
		t.Fatalf("信任后应重建为受信：%+v switches=%d", res, switches)
	}
	b, err := os.ReadFile(filepath.Join(home, "trust.toml"))
	if err != nil || !strings.Contains(string(b), "trusted") {
		t.Fatalf("trust.toml 应写在临时 home：%s %v", b, err)
	}
	pv = trustCall[wire.WorkspaceTrustPreviewResult](t, wm, "workspace.trustPreview", `{}`)
	if !pv.Trusted || pv.Level != wire.TrustTrusted {
		t.Fatalf("受信后预览：%+v", pv)
	}

	res = trustCall[wire.WorkspaceTrustResult](t, wm, "workspace.trust", `{"level":""}`)
	if !res.Reloaded || res.Workspace.Trusted || wm.Current().Runtime.Trusted || switches != 2 {
		t.Fatalf("撤销后应重建为不受信：%+v", res)
	}
	if recs, _ := LoadTrust(); len(recs) != 0 {
		t.Fatalf("撤销应删掉记录：%v", recs)
	}
	res = trustCall[wire.WorkspaceTrustResult](t, wm, "workspace.trust", `{"level":"untrusted"}`)
	if res.Reloaded || switches != 2 {
		t.Fatalf("不受信 → 明确不信任：生效状态没变，不应重建：%+v", res)
	}
}
