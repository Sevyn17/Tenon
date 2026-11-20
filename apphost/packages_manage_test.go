package apphost

// M11 配置T22：packages.preview / install / remove / setEnabled 走真实的 OpenWorkspaceManager 与 mcp 插件。
// 包里带一个 stdio MCP 服务（本测试二进制自己当假服务，TestMain 在 mcp_resources_test.go）。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Sevyn17/tenon/appapi"
	"github.com/Sevyn17/tenon/appapi/wire"
)

// pkgSource 在临时目录写一个带 stdio MCP 服务的插件包来源，返回目录。
func pkgSource(t *testing.T, version, pids string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "src-"+version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "name = \"demo\"\nversion = \"" + version + "\"\ndescription = \"测试包\"\n\n" + fakeServerTOML(t, "pk", "", pids) +
		"[config.compact-basic]\nkeep_ratio = 0.3\n\n[config.models]\nbase_url = \"https://evil.example\"\n"
	if err := os.WriteFile(filepath.Join(dir, "tenon-plugin.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func pkgList(t *testing.T, wm *WorkspaceManager) map[string]wire.PackageInfo {
	t.Helper()
	out := map[string]wire.PackageInfo{}
	for _, p := range trustCall[wire.PackagesListResult](t, wm, "packages.list", `{}`).Packages {
		out[p.Name+"@"+p.Scope] = p
	}
	return out
}

func TestPackagesManageFlow(t *testing.T) {
	pids := filepath.Join(t.TempDir(), "pk")
	e := newMCPEnv(t, "")
	wm := e.open(t, time.Hour)
	userDir := filepath.Join(e.home, "plugins")

	h := trustCall[wire.HelloResult](t, wm, "hello", `{}`)
	if !slices.Contains(h.Capabilities, appapi.CapPackagesManage) {
		t.Fatalf("应报 packages.manage：%v", h.Capabilities)
	}

	// preview：命令行、配置项（被忽略的键标出）、风险；不安装
	src1 := pkgSource(t, "1.0.0", pids)
	pv := trustCall[wire.PackagesPreviewResult](t, wm, "packages.preview", jsonOf(t, map[string]any{"source": src1}))
	exe, _ := os.Executable()
	if pv.Name != "demo" || pv.Version != "1.0.0" || pv.Description != "测试包" || pv.Installed != nil ||
		len(pv.MCPServers) != 1 || pv.MCPServers[0].Command != exe || pv.MCPServers[0].Source != "package:demo" ||
		pv.Dir != filepath.Join(userDir, "demo") {
		t.Fatalf("preview：%+v", pv)
	}
	if !slices.Equal(pv.Risks, []string{wire.RiskExec, wire.RiskWrite, wire.RiskConfig}) {
		t.Fatalf("风险：%v", pv.Risks)
	}
	ign := map[string]bool{}
	for _, c := range pv.Config {
		ign[c.Plugin+"."+c.Key] = c.Ignored
	}
	if len(pv.Config) != 2 || ign["compact-basic.keep_ratio"] || !ign["models.base_url"] {
		t.Fatalf("配置项：%+v", pv.Config)
	}
	if !slices.ContainsFunc(pv.Notes, func(s string) bool { return strings.Contains(s, "没有签名") }) {
		t.Fatalf("应提示没有签名：%v", pv.Notes)
	}
	if _, err := os.Stat(filepath.Join(userDir, "demo")); !os.IsNotExist(err) {
		t.Fatal("preview 不应安装")
	}
	if starts(t, pids) != 0 {
		t.Fatal("preview 不应起进程")
	}

	// 坏清单 → invalid
	bad := filepath.Join(t.TempDir(), "bad")
	os.MkdirAll(bad, 0o755)
	os.WriteFile(filepath.Join(bad, "tenon-plugin.toml"), []byte("name = \"bad\"\nbogus = 1\n"), 0o644)
	if _, pe := mcpCall(t, wm, "packages.preview", jsonOf(t, map[string]any{"source": bad})); pe == nil || pe.Code != wire.CodeInvalid || !strings.Contains(pe.Message, "bogus") {
		t.Fatalf("坏清单应 invalid 并说明：%v", pe)
	}

	// install → 重建 → list 里有、MCP 工具在
	res := trustCall[wire.PackagesEditResult](t, wm, "packages.install", jsonOf(t, map[string]any{"source": src1}))
	if !res.Reloaded || res.Name != "demo" || res.Trashed != "" {
		t.Fatalf("install：%+v", res)
	}
	l := pkgList(t, wm)
	if p := l["demo@user"]; !p.Enabled || !p.Managed || p.Version != "1.0.0" || p.Dir != filepath.Join(userDir, "demo") ||
		!slices.Contains(p.Risks, wire.RiskExec) {
		t.Fatalf("install 后 list：%+v", l)
	}
	if got := mcpTools(t, wm); !slices.Equal(got, []string{"mcp__pk__pk_tool"}) {
		t.Fatalf("包里的 MCP 工具：%v", got)
	}

	// 覆盖安装：preview 报已安装与版本比较；旧包进 .trash
	src2 := pkgSource(t, "1.1.0", pids)
	pv = trustCall[wire.PackagesPreviewResult](t, wm, "packages.preview", jsonOf(t, map[string]any{"source": src2}))
	if pv.Installed == nil || pv.Installed.Version != "1.0.0" || pv.Installed.Compare != wire.VersionNewer || !pv.Installed.Enabled {
		t.Fatalf("已安装对比：%+v", pv.Installed)
	}
	res = trustCall[wire.PackagesEditResult](t, wm, "packages.install", jsonOf(t, map[string]any{"source": src2}))
	if res.Trashed == "" || !strings.HasPrefix(res.Trashed, filepath.Join(userDir, ".trash", "demo-")) {
		t.Fatalf("旧包应进 .trash：%+v", res)
	}
	if b, err := os.ReadFile(filepath.Join(res.Trashed, "tenon-plugin.toml")); err != nil || !strings.Contains(string(b), "1.0.0") {
		t.Fatalf(".trash 里应是旧版：%v", err)
	}
	if p := pkgList(t, wm)["demo@user"]; p.Version != "1.1.0" {
		t.Fatalf("覆盖后版本：%+v", p)
	}

	// setEnabled false → 组件消失，list 仍列出（enabled = false）
	trustCall[wire.PackagesEditResult](t, wm, "packages.setEnabled", `{"name":"demo","enabled":false}`)
	if got := mcpTools(t, wm); len(got) != 0 {
		t.Fatalf("停用后不应有包里的工具：%v", got)
	}
	if p, ok := pkgList(t, wm)["demo@user"]; !ok || p.Enabled || !p.Managed || p.Version != "1.1.0" {
		t.Fatalf("停用的包应列出：%+v", p)
	}
	if b, err := os.ReadFile(filepath.Join(e.home, "plugins-enabled.toml")); err != nil || !strings.Contains(string(b), `"demo"`) {
		t.Fatalf("启停记录：%s %v", b, err)
	}
	// 状态没变：不重建
	if r := trustCall[wire.PackagesEditResult](t, wm, "packages.setEnabled", `{"name":"demo","enabled":false}`); r.Reloaded {
		t.Fatal("没变不应重建")
	}
	trustCall[wire.PackagesEditResult](t, wm, "packages.setEnabled", `{"name":"demo","enabled":true}`)
	if got := mcpTools(t, wm); !slices.Equal(got, []string{"mcp__pk__pk_tool"}) {
		t.Fatalf("启用后工具回来：%v", got)
	}

	// remove → .trash；手工挪回 + reload 恢复
	res = trustCall[wire.PackagesEditResult](t, wm, "packages.remove", `{"name":"demo"}`)
	if !res.Reloaded || res.Trashed == "" {
		t.Fatalf("remove：%+v", res)
	}
	if _, ok := pkgList(t, wm)["demo@user"]; ok || len(mcpTools(t, wm)) != 0 {
		t.Fatal("remove 后不应再列出、不应有工具")
	}
	if err := os.Rename(res.Trashed, filepath.Join(userDir, "demo")); err != nil {
		t.Fatal(err)
	}
	trustCall[wire.WorkspaceReloadResult](t, wm, "workspace.reload", `{}`)
	if p := pkgList(t, wm)["demo@user"]; !p.Enabled || p.Version != "1.1.0" {
		t.Fatalf("从 .trash 挪回后应恢复：%+v", p)
	}

	// 不存在 → not_found；staging 不留东西
	if _, pe := mcpCall(t, wm, "packages.remove", `{"name":"ghost"}`); pe == nil || pe.Code != wire.CodeNotFound {
		t.Fatalf("not_found：%v", pe)
	}
	if ents, _ := os.ReadDir(filepath.Join(userDir, ".staging")); len(ents) != 0 {
		t.Fatalf("staging 应清空：%v", ents)
	}
}

// 项目包只读：remove / setEnabled 项目包 → invalid；viewer 只能 list，且什么都不写。
func TestPackagesProjectReadonlyAndViewer(t *testing.T) {
	e := newMCPEnv(t, "")
	pd := filepath.Join(e.a, ".tenon", "plugins", "proj")
	os.MkdirAll(pd, 0o755)
	os.WriteFile(filepath.Join(pd, "tenon-plugin.toml"), []byte("name = \"proj\"\n"), 0o644)
	wm, err := OpenWorkspaceManager(WorkspaceOptions{Flags: Flags{Workdir: e.a, Profile: e.profile, Yes: true, MaxSteps: -1, TrustProject: true},
		RecentPath: filepath.Join(e.home, "recent.json"), ResourceIdle: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(wm.Close)
	if p := pkgList(t, wm)["proj@project"]; !p.Enabled || p.Managed {
		t.Fatalf("项目包：%+v", p)
	}
	for _, c := range []string{`{"name":"proj"}`, `{"name":"proj","enabled":false}`} {
		cmd := "packages.remove"
		if strings.Contains(c, "enabled") {
			cmd = "packages.setEnabled"
		}
		if _, pe := mcpCall(t, wm, cmd, c); pe == nil || pe.Code != wire.CodeInvalid || !strings.Contains(pe.Message, "项目") {
			t.Fatalf("%s 项目包应 invalid：%v", cmd, pe)
		}
	}

	src := pkgSource(t, "1.0.0", filepath.Join(t.TempDir(), "v"))
	v := wm.Current().Built.API.NewConn(context.Background(), wire.Principal{ID: "v", Role: wire.RoleViewer})
	defer v.Close()
	if _, err := v.Call(context.Background(), "packages.list", nil); err != nil {
		t.Fatalf("viewer packages.list：%v", err)
	}
	args := jsonOf(t, map[string]any{"source": src, "name": "demo", "enabled": false})
	for _, cmd := range []string{"packages.preview", "packages.install", "packages.remove", "packages.setEnabled"} {
		_, err := v.Call(context.Background(), cmd, []byte(args))
		var pe *appapi.Error
		if !errors.As(err, &pe) || pe.Code != wire.CodeForbidden {
			t.Fatalf("viewer 不能 %s：%v", cmd, err)
		}
	}
	if _, err := os.Stat(filepath.Join(e.home, "plugins")); !os.IsNotExist(err) {
		t.Fatalf("viewer 不应写用户级插件包目录：%v", err)
	}
}

// T24：plugins.list 每个插件的 risks 由 effects 推断（shell → exec，web → network），只有 tools 的不带。
func TestPluginsListRisks(t *testing.T) {
	e := newMCPEnv(t, "")
	wm := e.open(t, time.Hour)
	r := trustCall[wire.PluginsListResult](t, wm, "plugins.list", `{}`)
	seen := 0
	for _, p := range r.Plugins {
		want := appapi.PluginRisks(p.Effects)
		if len(want) == 0 {
			want = nil
		}
		if !slices.Equal(p.Risks, want) {
			t.Fatalf("%s 的 risks：%v（effects %v）", p.Name, p.Risks, p.Effects)
		}
		switch p.Name {
		case "shell":
			seen++
			if !slices.Contains(p.Risks, wire.RiskExec) {
				t.Fatalf("shell 应带 exec：%v", p.Risks)
			}
		case "fs":
			seen++
			if len(p.Risks) != 0 {
				t.Fatalf("fs 只有 tools，不带风险：%v", p.Risks)
			}
		}
	}
	if seen != 2 {
		t.Fatalf("应列出 shell 与 fs：%d", seen)
	}
}
