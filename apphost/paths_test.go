package apphost

import (
	"path/filepath"
	"testing"
)

// 不设 Paths 时位置与原来相同；设了就用设的值，清空后回到缺省。
func TestPathsDefaultsAndOverride(t *testing.T) {
	h := t.TempDir()
	t.Setenv("TENON_HOME", h)
	want := Paths{
		Trust:          filepath.Join(h, "trust.toml"),
		Recent:         filepath.Join(h, "workspaces.json"),
		Skills:         filepath.Join(h, "skills.toml"),
		MCPEnabled:     filepath.Join(h, "mcp-enabled.toml"),
		PluginsEnabled: filepath.Join(h, "plugins-enabled.toml"),
		ModesDir:       filepath.Join(h, "modes"),
		Permissions:    filepath.Join(h, "permissions.toml"),
	}
	if got := CurrentPaths(); got != want {
		t.Fatalf("缺省位置变了：\n got %+v\nwant %+v", got, want)
	}

	o := t.TempDir()
	SetPaths(Paths{Trust: filepath.Join(o, "t.toml"), Recent: filepath.Join(o, "r.json")})
	defer SetPaths(Paths{})
	if TrustPath() != filepath.Join(o, "t.toml") || recentFile("") != filepath.Join(o, "r.json") {
		t.Fatalf("覆盖未生效：TrustPath=%q recent=%q", TrustPath(), recentFile(""))
	}
	if got := CurrentPaths(); got.Skills != want.Skills || got.ModesDir != want.ModesDir {
		t.Fatalf("没设的项应保持缺省：%+v", got)
	}
	if recentFile(filepath.Join(o, "x.json")) != filepath.Join(o, "x.json") {
		t.Fatal("WorkspaceOptions.RecentPath 应优先")
	}

	SetPaths(Paths{})
	if got := CurrentPaths(); got != want {
		t.Fatalf("清空后应回到缺省：%+v", got)
	}
}
