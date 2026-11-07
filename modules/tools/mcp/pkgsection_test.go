package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tenon/runtime/pkgs"
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

// 插件包的 [[server]] 段由本组件解码与校验（2026-11-04 审查 P1-56：原先形状写死在 runtime/pkgs）。
func TestPackageServerSection(t *testing.T) {
	root := t.TempDir()
	p, err := pkgs.Parse(writePkg(t, root, "ok", "[[server]]\nname = \"fs\"\ncommand = \"${TENON_PLUGIN_DIR}/bin/fs\"\ntrust = \"readonly\"\n"), pkgs.ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	if ss := packageServers(p); len(ss) != 1 || ss[0].Name != "fs" || ss[0].Trust != TrustReadonly {
		t.Fatalf("解码结果不对：%+v", ss)
	}
	if c := p.Contents(); !strings.Contains(c, "MCP 服务 fs") {
		t.Fatalf("Contents=%q", c)
	}
	for name, c := range map[string]struct{ body, want string }{
		"both":  {"[[server]]\nname = \"a\"\ncommand = \"x\"\nurl = \"http://x\"\n", "command 或 url"},
		"trust": {"[[server]]\nname = \"a\"\ncommand = \"x\"\ntrust = \"all\"\n", "trust"},
		"typo":  {"[[server]]\nname = \"a\"\ncommand = \"x\"\ncomand = \"y\"\n", "未知字段"},
		"cwd":   {"[[server]]\nname = \"a\"\ncommand = \"x\"\ncwd = \"../out\"\n", "cwd"},
	} {
		if _, err := pkgs.Parse(writePkg(t, root, name, c.body), pkgs.ScopeUser); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s：err=%v，想要含 %q", name, err, c.want)
		}
	}
}
