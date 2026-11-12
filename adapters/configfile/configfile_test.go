package configfile

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Sevyn17/tenon/runtime/component"
)

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLayersOrderAndTrust(t *testing.T) {
	home, proj := t.TempDir(), t.TempDir()
	write(t, home, "hooks.toml", "[[hook]]\nevent = \"stop\"\ncommand = \"u\"\n")
	write(t, proj, "permissions.toml", "mode = \"read-only\"\n")

	layers, err := New(home, proj, false).Layers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(layers) != 2 {
		t.Fatalf("应有两层，得 %d", len(layers))
	}
	if layers[0].Origin != component.OriginUser || layers[1].Origin != component.OriginProject {
		t.Fatalf("用户层应在前：%s、%s", layers[0].Origin, layers[1].Origin)
	}
	if !layers[0].Trusted || layers[1].Trusted {
		t.Fatalf("只有项目层按传入的受信标记：%v %v", layers[0].Trusted, layers[1].Trusted)
	}
	if layers[0].File("hooks") == nil || layers[0].File("permissions") != nil {
		t.Fatal("用户层应只有 hooks")
	}
	if layers[1].File("permissions") == nil || layers[1].File("hooks") != nil {
		t.Fatal("项目层应只有 permissions")
	}
	// 往返解码应还原成组件认得的结构。
	var decoded struct {
		Hook []struct {
			Event   string `toml:"event"`
			Command string `toml:"command"`
		} `toml:"hook"`
	}
	if err := component.DecodeValue(layers[0].File("hooks"), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Hook) != 1 || decoded.Hook[0].Command != "u" {
		t.Fatalf("解码结果不对：%+v", decoded.Hook)
	}
}

func TestMissingFilesAreAbsent(t *testing.T) {
	layers, err := New(t.TempDir(), "", true).Layers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(layers) != 0 {
		t.Fatalf("没有配置文件时不应有层，得 %d", len(layers))
	}
}

func TestBadFileReportsPath(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "mcp.toml", "[[server]\n")
	if _, err := New(dir, "", true).Layers(context.Background()); err == nil {
		t.Fatal("语法错误应报错")
	}
}
