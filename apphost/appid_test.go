package apphost

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Sevyn17/tenon/runtime/appid"
)

// 应用身份改了以后，用户级目录、信任记录与提示里的命令名跟着变。
func TestAppIDChangesPaths(t *testing.T) {
	old := appid.Get()
	t.Cleanup(func() { appid.Set(old) })
	appid.Set(appid.ID{Name: "mortise", Dir: ".mortise", Env: "MORTISE"})
	h := t.TempDir()
	t.Setenv("MORTISE_HOME", h)
	t.Setenv("TENON_HOME", filepath.Join(h, "wrong"))
	if Home() != h || TrustPath() != filepath.Join(h, "trust.toml") {
		t.Fatalf("Home=%q TrustPath=%q", Home(), TrustPath())
	}
	root := t.TempDir()
	if appid.Project(root) != filepath.Join(root, ".mortise") {
		t.Fatalf("项目目录 %q", appid.Project(root))
	}
	if c := appid.Command("trust"); !strings.HasPrefix(c, "mortise ") {
		t.Fatalf("命令名 %q", c)
	}
}
