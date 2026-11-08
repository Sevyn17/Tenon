package apphost

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/appapi/wire"
)

// 旧版每个项目一个会话库：打开工作区时并入用户级库、记上工作区、旧库改名；再打开不会重复并。
func TestImportLegacyProjectStore(t *testing.T) {
	root := t.TempDir()
	// 旧版：库在项目里（用户目录指到项目的 .tenon，等于以前的位置）。
	t.Setenv("TENON_HOME", filepath.Join(root, ".tenon"))
	wm, err := OpenWorkspaceManager(WorkspaceOptions{Flags: Flags{Workdir: root, MaxSteps: -1}})
	if err != nil {
		t.Fatal(err)
	}
	wsCall[wire.SessionSnapshot](t, wm.Current().Built.API, "session.create", map[string]any{"session": "old1"})
	wm.Close()

	home := t.TempDir()
	t.Setenv("TENON_HOME", home)
	wm, err = OpenWorkspaceManager(WorkspaceOptions{Flags: Flags{Workdir: root, MaxSteps: -1}})
	if err != nil {
		t.Fatal(err)
	}
	notes := strings.Join(wm.Current().Runtime.Notes, "\n")
	if !strings.Contains(notes, "1 个旧会话并入") {
		t.Fatalf("应提示并入了旧会话：%q", notes)
	}
	list := wsCall[wire.ListResult](t, wm.Current().Built.API, "sessions.list", map[string]any{})
	if len(list.Items) != 1 || list.Items[0].ID != "old1" || list.Items[0].Workspace != WorkspaceKey(root) {
		t.Fatalf("并入后的会话：%+v", list.Items)
	}
	if _, err := os.Stat(filepath.Join(root, ".tenon", "state.db")); err == nil {
		t.Fatal("旧库应已改名")
	}
	if _, err := os.Stat(filepath.Join(root, ".tenon", "state.db"+LegacyImportedSuffix)); err != nil {
		t.Fatalf("旧库应改名为 .imported：%v", err)
	}
	wm.Close()

	wm, err = OpenWorkspaceManager(WorkspaceOptions{Flags: Flags{Workdir: root, MaxSteps: -1}})
	if err != nil {
		t.Fatal(err)
	}
	defer wm.Close()
	if notes := strings.Join(wm.Current().Runtime.Notes, "\n"); strings.Contains(notes, "旧会话") {
		t.Fatalf("第二次打开不应再并：%q", notes)
	}
	if l := wsCall[wire.ListResult](t, wm.Current().Built.API, "sessions.list", map[string]any{}); len(l.Items) != 1 {
		t.Fatalf("会话不应重复：%+v", l.Items)
	}
}
