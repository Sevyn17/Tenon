package apphost

// M11 配置T14：mcp.list / test / save / remove / setEnabled 走真实的 OpenWorkspaceManager 与 mcp 插件。
// 假 stdio MCP 服务是本测试二进制自己（TestMain 在 mcp_resources_test.go）。

import (
	"context"
	"encoding/json"
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

func mcpCall(t *testing.T, wm *WorkspaceManager, cmd, args string) (any, *appapi.Error) {
	t.Helper()
	res, err := wm.Current().Built.API.Call(context.Background(), cmd, json.RawMessage(args))
	if err != nil {
		var pe *appapi.Error
		if !errors.As(err, &pe) {
			t.Fatalf("%s：%v", cmd, err)
		}
		return nil, pe
	}
	return res, nil
}

func mcpList(t *testing.T, wm *WorkspaceManager) wire.MCPListResult {
	t.Helper()
	return trustCall[wire.MCPListResult](t, wm, "mcp.list", `{}`)
}

func serverIn(r wire.MCPListResult, name string) (wire.MCPServerInfo, bool) {
	for _, s := range r.Servers {
		if s.Name == name {
			return s, true
		}
	}
	return wire.MCPServerInfo{}, false
}

// fakeDraft mcp.save / mcp.test 的 server 参数：命令是本测试二进制，env 让它当假服务跑。
func fakeDraft(t *testing.T, name, pids string) map[string]any {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"name": name, "transport": "stdio", "command": exe, "timeout": 10,
		"env": map[string]any{"FAKE_MCP": "1", "FAKE_TOOL": name + "_tool", "FAKE_PID_FILE": pids}}
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// GUI 流程：test 草稿 → save → list 看到它和工具；env 值不回显；只改一个 env 键时其它键保留；revision 冲突；
// 按工作区停用后本工作区没有它的工具、另一工作区照常；remove。用户级文件里别的段和注释保留。
func TestMCPCommandsFlow(t *testing.T) {
	pidsKeep := filepath.Join(t.TempDir(), "keep")
	pidsS := filepath.Join(t.TempDir(), "s")
	e := newMCPEnv(t, "# 我自己的注释\n"+fakeServerTOML(t, "keep", "", pidsKeep)+"# keep 后面的注释\n")
	userFile := filepath.Join(e.home, "mcp.toml")
	wm := e.open(t, time.Hour)

	h := trustCall[wire.HelloResult](t, wm, "hello", `{}`)
	if !slices.Contains(h.Capabilities, appapi.CapMCP) {
		t.Fatalf("应报 mcp 能力：%v", h.Capabilities)
	}
	l0 := mcpList(t, wm)
	if !l0.Editable || l0.UserPath != userFile || l0.Revision == "" || len(l0.Servers) != 1 {
		t.Fatalf("初始列表：%+v", l0)
	}
	if k, _ := serverIn(l0, "keep"); k.State != wire.MCPStateConnected || len(k.Tools) != 1 || k.Tools[0].Name != "keep_tool" ||
		k.Tools[0].Description != "假工具" || k.Readonly || !slices.Contains(k.EnvKeys, "FAKE_MCP") {
		t.Fatalf("keep：%+v", k)
	}

	// test：草稿临时连一次，不保存
	tr := trustCall[wire.MCPTestResult](t, wm, "mcp.test", jsonOf(t, map[string]any{"server": fakeDraft(t, "s", pidsS)}))
	if !tr.OK || len(tr.Tools) != 1 || tr.Tools[0].Name != "s_tool" || tr.Tools[0].Public != "mcp__s__s_tool" {
		t.Fatalf("mcp.test：%+v", tr)
	}
	bad := trustCall[wire.MCPTestResult](t, wm, "mcp.test", `{"server":{"name":"nope","command":"/no/such/binary-xyz"}}`)
	if bad.OK || bad.Error == "" {
		t.Fatalf("连不上应 ok=false 带原因：%+v", bad)
	}
	if _, err := os.Stat(pidsS); err != nil {
		t.Fatalf("test 应真的起过进程：%v", err)
	}

	// 字段校验
	for _, args := range []string{
		`{"server":{"name":"","command":"x"}}`,
		`{"server":{"name":"x","transport":"stdio","url":"http://127.0.0.1:1"}}`,
		`{"server":{"name":"x","command":"a","url":"http://127.0.0.1:1"}}`,
		`{"server":{"name":"x","command":"a","scope":"tenant"}}`,
		`{"server":{"name":"x","command":"a","env":{"K":null}}}`,
	} {
		if _, pe := mcpCall(t, wm, "mcp.save", args); pe == nil || pe.Code != wire.CodeInvalid {
			t.Fatalf("%s 应 invalid：%v", args, pe)
		}
	}

	// save → 重建 → list 看到它和工具
	res := trustCall[wire.MCPEditResult](t, wm, "mcp.save", jsonOf(t, map[string]any{"server": fakeDraft(t, "s", pidsS), "revision": l0.Revision}))
	if !res.Reloaded || res.Revision == l0.Revision {
		t.Fatalf("save：%+v", res)
	}
	l1 := mcpList(t, wm)
	s, ok := serverIn(l1, "s")
	if !ok || s.State != wire.MCPStateConnected || len(s.Tools) != 1 || s.Tools[0].Name != "s_tool" || s.Source != "user" ||
		l1.Revision != res.Revision {
		t.Fatalf("save 后列表：%+v", l1)
	}
	if !slices.Equal(s.EnvKeys, []string{"FAKE_MCP", "FAKE_PID_FILE", "FAKE_TOOL"}) {
		t.Fatalf("env_keys：%v", s.EnvKeys)
	}
	if js := jsonOf(t, l1); strings.Contains(js, pidsS) || strings.Contains(js, "s_tool\",\"FAKE") {
		t.Fatalf("env 的值不应回显：%s", js)
	}
	body, _ := os.ReadFile(userFile)
	if !strings.Contains(string(body), "# 我自己的注释") || !strings.Contains(string(body), "# keep 后面的注释") ||
		!strings.Contains(string(body), `name = "keep"`) {
		t.Fatalf("别的段与注释应保留：\n%s", body)
	}

	// 旧 revision → conflict（文件不变）
	_, pe := mcpCall(t, wm, "mcp.save", jsonOf(t, map[string]any{"server": fakeDraft(t, "s", pidsS), "revision": l0.Revision}))
	if pe == nil || pe.Code != wire.CodeConflict || pe.Details["reason"] != wire.ConflictStale || pe.Details["revision"] != l1.Revision {
		t.Fatalf("旧 revision 应 conflict：%+v", pe)
	}

	// 只改 FAKE_TOOL：FAKE_MCP 给 null（保留）、FAKE_PID_FILE 不提（保留）；新工具名说明两个旧值都还在
	exe, _ := os.Executable()
	upd := map[string]any{"name": "s", "command": exe, "timeout": 10, "env": map[string]any{"FAKE_TOOL": "s2_tool", "FAKE_MCP": nil}}
	res = trustCall[wire.MCPEditResult](t, wm, "mcp.save", jsonOf(t, map[string]any{"server": upd, "revision": l1.Revision}))
	l2 := mcpList(t, wm)
	if s, _ := serverIn(l2, "s"); s.State != wire.MCPStateConnected || len(s.Tools) != 1 || s.Tools[0].Name != "s2_tool" || len(s.EnvKeys) != 3 {
		t.Fatalf("未给值的 env 键应保留：%+v", s)
	}
	if b, _ := os.ReadFile(userFile); !strings.Contains(string(b), pidsS) {
		t.Fatalf("FAKE_PID_FILE 的原值应保留在文件里：\n%s", b)
	}
	// 显式删除一个键
	rm := map[string]any{"name": "s", "command": exe, "timeout": 10, "env_remove": []string{"FAKE_PID_FILE"}}
	res = trustCall[wire.MCPEditResult](t, wm, "mcp.save", jsonOf(t, map[string]any{"server": rm, "revision": res.Revision}))
	if s, _ := serverIn(mcpList(t, wm), "s"); !slices.Equal(s.EnvKeys, []string{"FAKE_MCP", "FAKE_TOOL"}) || s.State != wire.MCPStateConnected {
		t.Fatalf("env_remove 应删掉键：%+v", s)
	}

	// 按工作区停用：A 没有 s 的工具，B 照常
	trustCall[wire.MCPEditResult](t, wm, "mcp.setEnabled", `{"name":"s","enabled":false,"scope":"workspace"}`)
	if got := mcpTools(t, wm); !slices.Equal(got, []string{"mcp__keep__keep_tool"}) {
		t.Fatalf("A 停用 s 后的工具：%v", got)
	}
	if s, _ := serverIn(mcpList(t, wm), "s"); s.Enabled || s.State != wire.MCPStateDisabled || s.DisabledScope != wire.MCPScopeWorkspace {
		t.Fatalf("A 里 s 应标停用：%+v", s)
	}
	if _, err := wm.Open(context.Background(), e.b); err != nil {
		t.Fatal(err)
	}
	if got := mcpTools(t, wm); !slices.Equal(got, []string{"mcp__keep__keep_tool", "mcp__s__s2_tool"}) {
		t.Fatalf("B 不受 A 的工作区记录影响：%v", got)
	}
	if _, err := wm.Open(context.Background(), e.a); err != nil {
		t.Fatal(err)
	}

	// remove
	_, pe = mcpCall(t, wm, "mcp.remove", `{"name":"ghost"}`)
	if pe == nil || pe.Code != wire.CodeNotFound {
		t.Fatalf("删不存在的应 not_found：%v", pe)
	}
	cur := mcpList(t, wm)
	res = trustCall[wire.MCPEditResult](t, wm, "mcp.remove", jsonOf(t, map[string]any{"name": "s", "revision": cur.Revision}))
	if !res.Reloaded {
		t.Fatalf("remove：%+v", res)
	}
	if _, ok := serverIn(mcpList(t, wm), "s"); ok {
		t.Fatal("remove 后不应再列出 s")
	}
	body, _ = os.ReadFile(userFile)
	if strings.Contains(string(body), `name = "s"`) || !strings.Contains(string(body), "# 我自己的注释") || !strings.Contains(string(body), `name = "keep"`) {
		t.Fatalf("remove 只删 s 段：\n%s", body)
	}
}

// viewer 只能 mcp.list；test / save / remove / setEnabled 被拒，且什么都不写、不起进程。
func TestMCPViewerForbidden(t *testing.T) {
	pids := filepath.Join(t.TempDir(), "v")
	e := newMCPEnv(t, "")
	wm := e.open(t, time.Hour)
	v := wm.Current().Built.API.NewConn(context.Background(), wire.Principal{ID: "v", Role: wire.RoleViewer})
	defer v.Close()
	if _, err := v.Call(context.Background(), "mcp.list", nil); err != nil {
		t.Fatalf("viewer mcp.list：%v", err)
	}
	draft := jsonOf(t, map[string]any{"server": fakeDraft(t, "s", pids), "name": "s", "enabled": false})
	for _, cmd := range []string{"mcp.test", "mcp.save", "mcp.remove", "mcp.setEnabled"} {
		_, err := v.Call(context.Background(), cmd, []byte(draft))
		var pe *appapi.Error
		if !errors.As(err, &pe) || pe.Code != wire.CodeForbidden {
			t.Fatalf("viewer 不能 %s：%v", cmd, err)
		}
	}
	if _, err := os.Stat(filepath.Join(e.home, "mcp.toml")); !os.IsNotExist(err) {
		t.Fatalf("viewer 不应写出 mcp.toml：%v", err)
	}
	if starts(t, pids) != 0 {
		t.Fatal("viewer 的 mcp.test 不应起进程")
	}
}

// 没有任何定义：列表为空、不起进程；项目未受信时项目定义不列并给说明。
func TestMCPListEmptyAndUntrustedNote(t *testing.T) {
	e := newMCPEnv(t, "")
	pd := filepath.Join(e.a, ".tenon")
	if err := os.MkdirAll(pd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pd, "mcp.toml"), []byte("[[server]]\nname = \"p\"\ncommand = \"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wm := e.open(t, time.Hour)
	l := mcpList(t, wm)
	if len(l.Servers) != 0 || l.ProjectPath == "" || !slices.ContainsFunc(l.Notes, func(s string) bool { return strings.Contains(s, "未受信") }) {
		t.Fatalf("未受信项目：%+v", l)
	}
	if n := len(wm.Resources().List()); n != 0 {
		t.Fatalf("没有可用定义时不应起实例：%d", n)
	}
}
