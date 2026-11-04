package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ybase/runtime/pkgs"
)

func writePkg(t *testing.T, root, name, body string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, pkgs.ManifestName), []byte("name = \""+name+"\"\n"+body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// 插件包的 [[hook]] 段由本组件解码与校验，matcher 解码时已编译（审查 P1-56）。
func TestPackageHookSection(t *testing.T) {
	root := t.TempDir()
	p, err := pkgs.Parse(writePkg(t, root, "ok", "[[hook]]\nevent = \"pre_tool_use\"\nmatcher = \"shell\"\ncommand = \"echo hi\"\n"), pkgs.ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	hs := packageHooks(p)
	if len(hs) != 1 || !hs[0].matches("shell") || hs[0].matches("shellx") {
		t.Fatalf("解码结果不对：%+v", hs)
	}
	if c := p.Contents(); !strings.Contains(c, "钩子 1 条") {
		t.Fatalf("Contents=%q", c)
	}
	for name, c := range map[string]struct{ body, want string }{
		"event":   {"[[hook]]\nevent = \"nope\"\ncommand = \"x\"\n", "不支持"},
		"matcher": {"[[hook]]\nevent = \"stop\"\ncommand = \"x\"\nmatcher = \"(\"\n", "matcher"},
		"nocmd":   {"[[hook]]\nevent = \"stop\"\n", "command"},
	} {
		if _, err := pkgs.Parse(writePkg(t, root, name, c.body), pkgs.ScopeUser); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s：err=%v，想要含 %q", name, err, c.want)
		}
	}
}
