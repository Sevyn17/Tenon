package apphost

// 工作区切换与旧版会话库并入的测试在产品侧（Mortise/client/mortise/workspace_test.go）。
//
// 原因：这两个测试走真实装配，需要一个提供 service:db 的存储插件。存储实现已迁出框架
// （docs/02 - 开发管理/20），框架不再登记任何存储插件，这里跑不起来。
// 它们验证的是「用户级会话库跨工作区共用」与「旧库并入后改名、不重复并」，
// 都依赖具体存储实现的持久化行为，放在实现方才有意义。
//
// 这里只测最近列表的读写（RecentWorkspaces、remember），不需要装配。

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Sevyn17/tenon/runtime/appid"
)

func TestRecentWorkspaces(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "workspaces.json")

	if got := RecentWorkspaces(p); got != nil {
		t.Fatalf("文件不存在时应为 nil，得到 %v", got)
	}

	// 文件里的顺序不一定是时间顺序：按 at 倒序返回
	if err := os.WriteFile(p, []byte(`[{"path":"/a","at":1},{"path":"/c","at":3},{"path":"/b","at":2}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, want := RecentWorkspaces(p), []string{"/c", "/b", "/a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}

	if err := os.WriteFile(p, []byte(`not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := RecentWorkspaces(p); got != nil {
		t.Fatalf("坏文件应为 nil，得到 %v", got)
	}
}

// remember 写进去的，RecentWorkspaces 读得出来：新的在前，同一目录去重。
func TestRecentWorkspacesRemember(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "workspaces.json") // 目录不存在时 remember 自己建
	m := &WorkspaceManager{o: WorkspaceOptions{RecentPath: p}}
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	m.remember(a)
	m.remember(b)
	m.remember(a)
	if got, want := RecentWorkspaces(p), []string{a, b}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

// path 为空时读用户级目录（Home）的 workspaces.json。
func TestRecentWorkspacesDefaultPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv(appid.Get().EnvName("HOME"), home)
	if err := os.WriteFile(filepath.Join(home, "workspaces.json"), []byte(`[{"path":"/x","at":5}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, want := RecentWorkspaces(""), []string{"/x"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}
