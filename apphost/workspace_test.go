package apphost

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"tenon/appapi"
	"tenon/appapi/wire"
)

func wsCall[T any](t *testing.T, api *appapi.App, cmd string, args any) T {
	t.Helper()
	b, _ := json.Marshal(args)
	res, err := api.Call(context.Background(), cmd, b)
	if err != nil {
		t.Fatalf("%s：%v", cmd, err)
	}
	var out T
	raw, _ := json.Marshal(res)
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// 工作区切换：每个目录一套运行时与会话库；新 App 换上时通知传输层；最近列表记住打开过的目录；
// 坏路径不动当前工作区；同一目录不重建。
func TestWorkspaceManagerSwitch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TENON_HOME", home)
	a, b := t.TempDir(), t.TempDir()
	var swapped []*appapi.App
	wm, err := OpenWorkspaceManager(WorkspaceOptions{Flags: Flags{Workdir: a, MaxSteps: -1},
		OnSwitch: func(api *appapi.App) { swapped = append(swapped, api) }})
	if err != nil {
		t.Fatal(err)
	}
	defer wm.Close()
	first := wm.Current()
	h := wsCall[wire.HelloResult](t, first.Built.API, "hello", map[string]any{})
	if NormPath(h.Workdir) != NormPath(a) {
		t.Fatalf("hello.workdir = %s，应为 %s", h.Workdir, a)
	}
	found := false
	for _, c := range h.Capabilities {
		found = found || c == "workspaces"
	}
	if !found {
		t.Fatalf("capabilities 应带 workspaces：%v", h.Capabilities)
	}
	wsCall[wire.SessionSnapshot](t, first.Built.API, "session.create", map[string]any{"session": "in-a"})

	// 坏路径：报错，留在原工作区，不通知。
	if _, err := first.Built.API.Call(context.Background(), "workspace.open", json.RawMessage(`{"path":"`+filepath.ToSlash(filepath.Join(a, "nope"))+`"}`)); err == nil {
		t.Fatal("不存在的目录应报错")
	}
	if wm.Current() != first || len(swapped) != 0 {
		t.Fatal("坏路径不应切换")
	}
	// 同一目录：不重建、不通知。
	wsCall[wire.WorkspaceOpenResult](t, first.Built.API, "workspace.open", map[string]any{"path": a})
	if wm.Current() != first || len(swapped) != 0 {
		t.Fatal("同一目录不应重建")
	}

	r := wsCall[wire.WorkspaceOpenResult](t, first.Built.API, "workspace.open", map[string]any{"path": b})
	if !r.Workspace.Current || NormPath(r.Workspace.Path) != NormPath(b) {
		t.Fatalf("open 结果：%+v", r.Workspace)
	}
	second := wm.Current()
	if second == first || len(swapped) != 1 || swapped[0] != second.Built.API {
		t.Fatal("应换上新 App 并通知一次")
	}
	h2 := wsCall[wire.HelloResult](t, second.Built.API, "hello", map[string]any{})
	if NormPath(h2.Workdir) != NormPath(b) {
		t.Fatalf("新 hello.workdir = %s", h2.Workdir)
	}
	// 一个用户级库：b 的列表里没有 a 的会话，列全部工作区时有；在 b 里打开 a 的会话被拒绝（工具会在 b 的目录里跑）。
	list := wsCall[wire.ListResult](t, second.Built.API, "sessions.list", map[string]any{})
	for _, it := range list.Items {
		if it.ID == "in-a" {
			t.Fatal("b 的会话列表里不应有 a 的会话")
		}
	}
	allWs := wsCall[wire.ListResult](t, second.Built.API, "sessions.list", map[string]any{"all_workspaces": true})
	found = false
	for _, it := range allWs.Items {
		found = found || (it.ID == "in-a" && it.Workspace == WorkspaceKey(a))
	}
	if !found {
		t.Fatalf("all_workspaces 应列出 a 的会话并带工作区：%+v", allWs.Items)
	}
	if _, err := second.Built.API.Call(context.Background(), "session.send", json.RawMessage(`{"session":"in-a","text":"hi","client_id":"x1"}`)); err == nil {
		t.Fatal("在 b 里不应能给 a 的会话发消息")
	}
	if _, err := os.Stat(filepath.Join(home, "state.db")); err != nil {
		t.Fatalf("会话库应在用户目录：%v", err)
	}
	for _, d := range []string{a, b} {
		if _, err := os.Stat(filepath.Join(d, ".tenon")); err == nil {
			t.Fatalf("项目目录 %s 里不应再建 .tenon", d)
		}
	}

	ws := wsCall[wire.WorkspaceListResult](t, second.Built.API, "workspace.list", map[string]any{})
	if len(ws.Items) != 2 || !ws.Items[0].Current || NormPath(ws.Items[0].Path) != NormPath(b) || NormPath(ws.Items[1].Path) != NormPath(a) {
		t.Fatalf("workspace.list：%+v", ws.Items)
	}

	// 切回 a：a 的会话还在。
	wsCall[wire.WorkspaceOpenResult](t, second.Built.API, "workspace.open", map[string]any{"path": a})
	snap := wsCall[wire.SessionSnapshot](t, wm.Current().Built.API, "session.get", map[string]any{"session": "in-a"})
	if snap.ID != "in-a" {
		t.Fatalf("切回 a 后应能读到原来的会话：%+v", snap)
	}
}
